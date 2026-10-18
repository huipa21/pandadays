package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"pandadays/internal/days"
)

// homeNow is a fixed clock for home and card tests (user timezone set to UTC).
var homeNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func homeServer(t *testing.T) (*Server, int64, string) {
	t.Helper()
	s, db, u, token := authenticatedServer(t)
	if _, err := db.Exec(`UPDATE users SET timezone = 'UTC' WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return homeNow }
	return s, u.ID, token
}

func mustDay(t *testing.T, s *Server, owner int64, in days.DayInput) days.Day {
	t.Helper()
	d, err := s.days.Create(context.Background(), owner, in, homeNow)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

var itemTitle = regexp.MustCompile(`<span class="day-item-title">([^<]*)</span>`)

func listedTitles(body string) []string {
	var out []string
	for _, m := range itemTitle.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

func htmxGet(s *Server, path, token string, extra map[string]string) *httptest.ResponseRecorder {
	r := sessionRequest("GET", path, token)
	r.Header.Set("HX-Request", "true")
	for k, v := range extra {
		r.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	return rr
}

func TestHomeToggleFilteringAndOrdering(t *testing.T) {
	s, owner, token := homeServer(t)
	for _, in := range []days.DayInput{
		{Title: "Far", Date: "2027-03-01"},
		{Title: "Soon", Date: "2026-10-20"},
		{Title: "Today", Date: "2026-10-18"},
		{Title: "Tonight", Date: "2026-10-18", Time: "20:00"},
		{Title: "This morning", Date: "2026-10-18", Time: "08:00"},
		{Title: "Last week", Date: "2026-10-11"},
		{Title: "Ancient", Date: "1999-12-31"},
		{Title: "Birthday", Date: "1990-11-01", Repeat: days.RepeatYearly},
	} {
		mustDay(t, s, owner, in)
	}
	future := []string{"Today", "Tonight", "Soon", "Birthday", "Far"}
	past := []string{"This morning", "Last week", "Ancient"}
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"/", future},
		{"/?when=future", future},
		{"/?when=bogus", future},
		{"/?when=past", past},
	} {
		rr := get(s.Handler(), tc.path, token)
		if rr.Code != 200 {
			t.Fatalf("GET %s: %d", tc.path, rr.Code)
		}
		if got := listedTitles(rr.Body.String()); strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("GET %s:\n got %v\nwant %v", tc.path, got, tc.want)
		}
	}

	body := get(s.Handler(), "/", token).Body.String()
	for _, want := range []string{
		`<a href="/?when=future" hx-get="/?when=future" hx-target="#home-days" hx-swap="outerHTML" hx-push-url="true" aria-current="page">`,
		`<span class="day-item-label">2 days left</span>`,
		`<span class="day-item-label">8h 0m left</span>`,
		// Repeating days show the effective (next) target, not the original date.
		`<time datetime="2026-11-01">2026-11-01</time> · ↻ Every year`,
		`class="day-item day-today"`,
		`class="day-item day-future"`,
		`<a class="btn" href="/days/new">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("future list missing %q", want)
		}
	}
	pastBody := get(s.Handler(), "/?when=past", token).Body.String()
	for _, want := range []string{`class="day-item day-past"`, `<span class="day-item-label">7 days ago</span>`,
		`<a href="/?when=past" hx-get="/?when=past" hx-target="#home-days" hx-swap="outerHTML" hx-push-url="true" aria-current="page">`} {
		if !strings.Contains(pastBody, want) {
			t.Errorf("past list missing %q", want)
		}
	}
}

func TestHomeHTMXPartial(t *testing.T) {
	s, owner, token := homeServer(t)
	mustDay(t, s, owner, days.DayInput{Title: "Gone", Date: "2026-01-01"})
	mustDay(t, s, owner, days.DayInput{Title: "Coming", Date: "2027-01-01"})
	rr := htmxGet(s, "/?when=past", token, nil)
	body := rr.Body.String()
	if rr.Code != 200 || strings.Contains(body, "<html") || !strings.HasPrefix(body, `<div id="home-days"`) {
		t.Fatalf("partial: %d\n%s", rr.Code, body)
	}
	if got := listedTitles(body); len(got) != 1 || got[0] != "Gone" {
		t.Fatalf("partial titles: %v", got)
	}
	if vary := strings.Join(rr.Header().Values("Vary"), ","); !strings.Contains(vary, "HX-Request") {
		t.Errorf("Vary = %q", vary)
	}
	// History-restore misses need the full page.
	full := htmxGet(s, "/?when=past", token, map[string]string{"HX-History-Restore-Request": "true"})
	if !strings.Contains(full.Body.String(), "<html") {
		t.Error("history restore did not get a full page")
	}
}

func TestHomeEmptyStates(t *testing.T) {
	s, _, token := homeServer(t)
	if body := get(s.Handler(), "/", token).Body.String(); !strings.Contains(body, "No upcoming days yet.") {
		t.Error("future empty state missing")
	}
	if body := get(s.Handler(), "/?when=past", token).Body.String(); !strings.Contains(body, "No past days yet.") {
		t.Error("past empty state missing")
	}
}

func TestHomeOwnActiveDaysOnlyWithChips(t *testing.T) {
	s, owner, token := homeServer(t)
	ctx := context.Background()
	col, _ := s.collections.Create(ctx, owner, days.GroupInput{Name: "Family", Color: "#112233"}, homeNow)
	tag, _ := s.tags.Create(ctx, owner, days.GroupInput{Name: "Party"}, homeNow)
	mine := mustDay(t, s, owner, days.DayInput{Title: "Mine <3", Date: "2027-01-01", CollectionID: col.ID,
		TagIDs: []int64{tag.ID}, StyleColor: "#abcdef", StyleIcon: "🎈", Comment: "secret comment"})
	archived := mustDay(t, s, owner, days.DayInput{Title: "Archived one", Date: "2027-01-02"})
	if _, err := s.db.Exec(`UPDATE days SET archived_at = ? WHERE id = ?`, homeNow.Unix(), archived.ID); err != nil {
		t.Fatal(err)
	}
	bob, _ := secondUser(t, s)
	mustDay(t, s, bob.ID, days.DayInput{Title: "Bob's day", Date: "2027-01-01"})

	body := get(s.Handler(), "/", token).Body.String()
	if got := listedTitles(body); len(got) != 1 || got[0] != "Mine &lt;3" {
		t.Fatalf("titles: %v", got)
	}
	for _, want := range []string{
		`href="/days/` + strconv.FormatInt(mine.ID, 10) + `"`,
		`<rect width="1" height="1" fill="#abcdef"/>`,
		`🎈`,
		`fill="#112233"/></svg> Family</span>`,
		`Party</span>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home missing %q", want)
		}
	}
	if strings.Contains(body, "secret comment") {
		t.Error("comment shown in list view")
	}
}

func TestDayCardView(t *testing.T) {
	s, owner, token := homeServer(t)
	trip := mustDay(t, s, owner, days.DayInput{Title: "Trip", Date: "2028-01-20", Comment: "pack <light>"})
	body := get(s.Handler(), dayPath(trip.ID), token).Body.String()
	for _, want := range []string{
		`class="card day-card day-future"`,
		`<span class="countdown-big">459</span> <span class="countdown-caption">days left</span>`,
		`<li><strong>1</strong> year</li><li><strong>3</strong> months</li><li><strong>2</strong> days</li>`,
		`<rect width="1" height="1"/>`,
		`⏳`,
		`pack &lt;light&gt;`,
		`id="attachments"`,
		"No attachments yet.",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("card missing %q", want)
		}
	}
	if strings.Contains(body, "Next occurrence") {
		t.Error("non-repeating card shows next occurrence")
	}

	bday := mustDay(t, s, owner, days.DayInput{Title: "Birthday", Date: "2023-10-25", Time: "09:30",
		Repeat: days.RepeatYearly, StyleColor: "#ff0000", StyleIcon: "🎂"})
	body = get(s.Handler(), dayPath(bday.ID), token).Body.String()
	for _, want := range []string{
		`<span class="countdown-big">7</span> <span class="countdown-caption">days left</span>`,
		`<li><strong>6</strong> days</li><li><strong>21</strong> hours</li><li><strong>30</strong> minutes</li>`,
		"🎉 3rd anniversary",
		"Since the original date (2023-10-25)",
		`<li><strong>2</strong> years</li><li><strong>11</strong> months</li>`,
		`Next occurrence</dt><dd><time datetime="2026-10-25">2026-10-25</time> 09:30</dd>`,
		`fill="#ff0000"`,
		`🎂`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("repeating card missing %q", want)
		}
	}

	past := mustDay(t, s, owner, days.DayInput{Title: "Done", Date: "2026-10-17"})
	if _, err := s.db.Exec(`UPDATE days SET archived_at = ? WHERE id = ?`, homeNow.Unix(), past.ID); err != nil {
		t.Fatal(err)
	}
	body = get(s.Handler(), dayPath(past.ID), token).Body.String()
	for _, want := range []string{`class="card day-card day-past day-archived"`, `<span class="countdown-big">1</span> <span class="countdown-caption">day ago</span>`,
		`<span class="badge badge-archived">📦 Archived</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("archived card missing %q", want)
		}
	}

	today := mustDay(t, s, owner, days.DayInput{Title: "Now", Date: "2026-10-18"})
	body = get(s.Handler(), dayPath(today.ID), token).Body.String()
	if !strings.Contains(body, `<span class="countdown-big">Today</span></p>`) || strings.Contains(body, `class="breakdown"`) {
		t.Error("today card should show Today without a breakdown")
	}

	if _, err := s.db.Exec(`UPDATE users SET lang = 'zh' WHERE id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	body = get(s.Handler(), dayPath(bday.ID), token).Body.String()
	for _, want := range []string{`<span class="countdown-caption">天后</span>`, "🎉 3 周年", `<strong>21</strong> 小时`} {
		if !strings.Contains(body, want) {
			t.Errorf("zh card missing %q", want)
		}
	}
}

func TestOrdinalNumber(t *testing.T) {
	for n, want := range map[int]string{1: "1st", 2: "2nd", 3: "3rd", 4: "4th", 11: "11th", 12: "12th", 13: "13th", 21: "21st", 102: "102nd", 111: "111th"} {
		if got := ordinalNumber("en", n); got != want {
			t.Errorf("ordinalNumber(en, %d) = %q, want %q", n, got, want)
		}
	}
	if got := ordinalNumber("zh", 3); got != "3" {
		t.Errorf("zh ordinal = %q", got)
	}
}

func TestHomeRequiresLogin(t *testing.T) {
	s, _, _ := homeServer(t)
	for _, p := range []string{"/", "/?when=past"} {
		if rr := get(s.Handler(), p, ""); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
			t.Errorf("GET %s anonymous: %d", p, rr.Code)
		}
	}
}

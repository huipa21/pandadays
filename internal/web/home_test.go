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
		`<span class="day-item-label">08:00 left</span>`,
		// Repeating days show the effective (next) target, not the original date.
		`<time datetime="2026-11-01">2026-11-01</time> · <span aria-hidden="true">↻</span> Every year`,
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

func TestDayListsNeverShowReservedGroups(t *testing.T) {
	s, owner, token := homeServer(t)
	ctx := context.Background()
	defaultTag, err := s.tags.DefaultID(ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	custom, err := s.tags.Create(ctx, owner, days.GroupInput{Name: "Visible tag"}, homeNow)
	if err != nil {
		t.Fatal(err)
	}
	plain := mustDay(t, s, owner, days.DayInput{Title: "Only defaults", Date: "2027-01-01"})
	mustDay(t, s, owner, days.DayInput{Title: "Mixed tags", Date: "2027-01-02", TagIDs: []int64{defaultTag, custom.ID}})
	for _, path := range []string{"/", "/?q=Only", "/tags/" + strconv.FormatInt(defaultTag, 10)} {
		body := get(s.Handler(), path, token).Body.String()
		start := strings.Index(body, `class="day-list"`)
		if start < 0 {
			t.Fatalf("%s: missing day list", path)
		}
		list := body[start:]
		if strings.Contains(list, `Default</span>`) {
			t.Errorf("%s shows reserved labels", path)
		}
	}
	body := get(s.Handler(), "/", token).Body.String()
	if !strings.Contains(body, "Visible tag</span>") {
		t.Error("non-default tag hidden")
	}
	if err := s.days.SetArchived(ctx, owner, plain.ID, true, homeNow); err != nil {
		t.Fatal(err)
	}
	if body := get(s.Handler(), "/archive", token).Body.String(); strings.Contains(body, "Default</span>") {
		t.Error("archive shows reserved labels")
	}
}

func TestDayCardView(t *testing.T) {
	s, owner, token := homeServer(t)
	trip := mustDay(t, s, owner, days.DayInput{Title: "Trip", Date: "2028-01-20", Comment: "pack <light>"})
	body := get(s.Handler(), dayPath(trip.ID), token).Body.String()
	for _, want := range []string{
		`class="card day-card day-future"`,
		`class="day-calendar-date"><time datetime="2028-01-20">2028-01-20</time>`,
		`<p class="countdown-caption">left</p>`,
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
	previous := -1
	for _, marker := range []string{`class="day-card-head"`, `class="day-calendar-date"`, `class="day-countdown"`, `class="day-meta"`, `class="day-comment"`, `id="attachments"`} {
		pos := strings.Index(body, marker)
		if pos <= previous {
			t.Errorf("card section %q missing or out of order", marker)
		}
		previous = pos
	}
	for _, unwanted := range []string{`class="day-facts"`, `href="/collections/`, `href="/tags/`, `danger-zone`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("card unexpectedly includes %q", unwanted)
		}
		for _, want := range []string{
			`</article>`,
			`<section class="card day-menu-section day-attachments" id="attachments" tabindex="-1" aria-labelledby="attachments-heading">`,
			`<section class="card day-menu-section day-sharing" id="day-sharing" tabindex="-1" aria-labelledby="sharing-heading">`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("menu-only controls missing %q", want)
			}
		}
		if strings.Index(body, `</article>`) > strings.Index(body, `id="attachments"`) {
			t.Error("attachments should be outside the calendar card")
		}
		if strings.Contains(body, `class="day-section-toggle"`) {
			t.Error("owner card should not duplicate Share or Attachment menu entries with section toggles")
		}
	}
	menuStart := strings.Index(body, `class="dropdown day-actions"`)
	menuEnd := strings.Index(body[menuStart:], `</details>`) + menuStart
	menu := body[menuStart:menuEnd]
	for _, want := range []string{">Edit</a>", ">Archive</button>", ">Delete</button>", ">Share</a>", ">Attachment</a>", `data-confirm="Delete this day? This cannot be undone."`, `name="csrf_token"`, `data-expand-section="attachments"`, `data-expand-section="day-sharing"`} {
		if !strings.Contains(menu, want) {
			t.Errorf("actions menu missing %q", want)
		}
	}

	bday := mustDay(t, s, owner, days.DayInput{Title: "Birthday", Date: "2023-10-25", Time: "09:30",
		Repeat: days.RepeatYearly, StyleColor: "#ff0000", StyleIcon: "🎂"})
	body = get(s.Handler(), dayPath(bday.ID), token).Body.String()
	for _, want := range []string{
		`<p class="countdown-caption">left</p>`,
		`<li><strong>6</strong> days</li><li class="day-clock">21:30</li>`,
		`<span aria-hidden="true">🎉</span> 3rd anniversary`,
		"Since the original date (2023-10-25)",
		`<li><strong>2</strong> years</li><li><strong>11</strong> months</li>`,
		`Next occurrence: <time datetime="2026-10-25">2026-10-25</time> 09:30</p>`,
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
	for _, want := range []string{`class="card day-card day-past day-archived"`, `<li><strong>1</strong> day</li>`, `<p class="countdown-caption">ago</p>`,
		`/unarchive"`, `>Unarchive</button>`,
		`<span class="badge badge-archived"><span aria-hidden="true">📦</span> Archived</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("archived card missing %q", want)
		}
	}

	today := mustDay(t, s, owner, days.DayInput{Title: "Now", Date: "2026-10-18"})
	body = get(s.Handler(), dayPath(today.ID), token).Body.String()
	if !strings.Contains(body, `<p class="countdown-caption">Today</p>`) || strings.Contains(body, `class="breakdown"`) {
		t.Error("today card should show Today without a breakdown")
	}

	if _, err := s.db.Exec(`UPDATE users SET lang = 'zh' WHERE id = ?`, owner); err != nil {
		t.Fatal(err)
	}
	body = get(s.Handler(), dayPath(bday.ID), token).Body.String()
	for _, want := range []string{`<p class="countdown-caption">后</p>`, `<span aria-hidden="true">🎉</span> 3 周年`, `<li class="day-clock">21:30</li>`} {
		if !strings.Contains(body, want) {
			t.Errorf("zh card missing %q", want)
		}
	}
}

func TestDayClockDisplay(t *testing.T) {
	s, owner, token := homeServer(t)
	s.now = func() time.Time { return homeNow.Add(15 * time.Second) }
	for _, tc := range []struct {
		title, date, clock, wantClock, direction, listLabel string
	}{
		{"Under a minute", "2026-10-18", "12:01", "00:00", "left", "00:00 left"},
		{"Under a day", "2026-10-19", "12:00", "23:59", "left", "23:59 left"},
		{"Over a day", "2026-10-19", "12:01", "00:00", "left", "1 day left"},
		{"Elapsed", "2026-10-18", "09:05", "02:55", "ago", "02:55 ago"},
		{"Long elapsed", "2023-08-16", "09:05", "02:55", "ago", ""},
	} {
		t.Run(tc.title, func(t *testing.T) {
			d := mustDay(t, s, owner, days.DayInput{Title: tc.title, Date: tc.date, Time: tc.clock})
			rr := get(s.Handler(), dayPath(d.ID), token)
			body := rr.Body.String()
			for _, want := range []string{`<li class="day-clock">` + tc.wantClock + `</li>`, `<p class="countdown-caption">` + tc.direction + `</p>`} {
				if rr.Code != 200 || !strings.Contains(body, want) {
					t.Errorf("detail status %d missing %q", rr.Code, want)
				}
			}
			if tc.listLabel != "" {
				path := "/"
				if tc.direction == "ago" {
					path = "/?when=past"
				}
				if body := get(s.Handler(), path, token).Body.String(); !strings.Contains(body, `class="day-item-label">`+tc.listLabel+`</span>`) {
					t.Errorf("list missing %q", tc.listLabel)
				}
			}
		})
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

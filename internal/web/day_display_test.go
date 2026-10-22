package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"pandadays/internal/days"
	"pandadays/internal/share"
)

func TestDayDisplayMode(t *testing.T) {
	s, owner, token := homeServer(t)
	s.now = func() time.Time { return time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC) }
	d := mustDay(t, s, owner, days.DayInput{Title: "Two months one day", Date: "2026-08-02"})
	for _, tc := range []struct {
		query, want, toggle, label string
	}{
		{"", `<li><strong>2</strong> months</li><li><strong>1</strong> day</li>`, dayPath(d.ID) + "?display=days", "Show total days"},
		{"?display=days", `<li><strong>62</strong> days</li>`, dayPath(d.ID), "Show years, months and days"},
		{"?display=invalid", `<li><strong>2</strong> months</li><li><strong>1</strong> day</li>`, dayPath(d.ID) + "?display=days", "Show total days"},
	} {
		rr := get(s.Handler(), dayPath(d.ID)+tc.query, token)
		body := rr.Body.String()
		for _, want := range []string{tc.want, `class="day-display-toggle" href="` + tc.toggle + `" aria-label="` + tc.label + `"`} {
			if rr.Code != http.StatusOK || !strings.Contains(body, want) {
				t.Errorf("%s: status %d missing %q", tc.query, rr.Code, want)
			}
		}
	}
	// Choosing one card's mode never changes another card's default.
	other := mustDay(t, s, owner, days.DayInput{Title: "Other", Date: "2026-08-02"})
	if body := get(s.Handler(), dayPath(other.ID), token).Body.String(); !strings.Contains(body, `<strong>2</strong> months`) {
		t.Error("another card lost its calendar default")
	}
	body := get(s.Handler(), dayPath(d.ID)+"?display=days&lang=zh", token).Body.String()
	for _, want := range []string{`<li><strong>62</strong> 天</li>`, `aria-label="按年月日显示"`} {
		if !strings.Contains(body, want) {
			t.Errorf("Chinese total-days card missing %q", want)
		}
	}
}

func TestDayDisplayTodayTimedAndRepeating(t *testing.T) {
	s, owner, token := homeServer(t)
	s.now = func() time.Time { return time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC) }
	for _, tc := range []struct {
		in   days.DayInput
		want []string
	}{
		{days.DayInput{Title: "Today", Date: "2025-03-01"}, []string{`<li><strong>0</strong> days</li>`, `>Today</p>`}},
		{days.DayInput{Title: "Tomorrow", Date: "2025-03-02"}, []string{`<li><strong>1</strong> day</li>`}},
		{days.DayInput{Title: "Timed", Date: "2025-04-02", Time: "11:30"}, []string{`<li><strong>31</strong> days</li>`, `<li class="day-clock">23:30</li>`}},
		{days.DayInput{Title: "Repeating", Date: "2024-02-29", Repeat: days.RepeatYearly}, []string{`<li><strong>364</strong> days</li>`, `<li><strong>366</strong> days</li>`, `Since the original date (2024-02-29)`}},
	} {
		d := mustDay(t, s, owner, tc.in)
		body := get(s.Handler(), dayPath(d.ID)+"?display=days", token).Body.String()
		for _, want := range tc.want {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %q", tc.in.Title, want)
			}
		}
	}
}

func TestArchivedDayFreezesUntilPlayed(t *testing.T) {
	ctx := context.Background()
	s, owner, token := homeServer(t)
	_, bobToken := secondUser(t, s)
	archivedAt := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	d := mustDay(t, s, owner, days.DayInput{Title: "Frozen", Date: "2026-05-02"})
	if err := s.days.SetArchived(ctx, owner, d.ID, true, archivedAt); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return archivedAt.AddDate(0, 0, 10) }
	path := dayPath(d.ID)
	check := func(step string, wants ...string) {
		t.Helper()
		body := get(s.Handler(), path+"?display=days", token).Body.String() + get(s.Handler(), "/archive", token).Body.String()
		for _, want := range wants {
			if !strings.Contains(body, want) {
				t.Errorf("%s: missing %q", step, want)
			}
		}
	}
	check("paused", `<li><strong>30</strong> days</li>`, `<span class="day-item-label">30 days ago</span>`,
		`action="`+path+`/play"`, `aria-label="Paused at archive time. Resume counting"`,
		`<input type="hidden" name="display" value="days">`, `class="day-created">Created <time datetime="`)

	if rr := sessionPost(t, s, path+"/play", bobToken, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("foreign play: %d", rr.Code)
	}
	rr := sessionPost(t, s, path+"/play", token, url.Values{"display": {"days"}})
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != path+"?display=days" {
		t.Fatalf("play: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	check("live", `<li><strong>40</strong> days</li>`, `<span class="day-item-label">40 days ago</span>`, `action="`+path+`/pause"`)

	if rr := sessionPost(t, s, path+"/pause", token, nil); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != path {
		t.Fatalf("pause: %d %q", rr.Code, rr.Header().Get("Location"))
	}
	check("paused again", `<li><strong>30</strong> days</li>`)

	active := mustDay(t, s, owner, days.DayInput{Title: "Active", Date: "2026-05-02"})
	if rr := sessionPost(t, s, dayPath(active.ID)+"/play", token, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("play on active day: %d", rr.Code)
	}
	if body := get(s.Handler(), dayPath(active.ID), token).Body.String(); strings.Contains(body, "day-play-toggle") {
		t.Error("active day shows play toggle")
	}
}

func TestDayDisplayAccessAndArchive(t *testing.T) {
	s, owner, token := homeServer(t)
	bob, bobToken := secondUser(t, s)
	d := mustDay(t, s, owner, days.DayInput{Title: "Authorized display", Date: "2026-08-17"})
	path := dayPath(d.ID) + "?display=days"
	if rr := get(s.Handler(), path, bobToken); rr.Code != http.StatusNotFound {
		t.Fatalf("non-owner total-days card: %d", rr.Code)
	}
	if rr := get(s.Handler(), path, ""); rr.Code != http.StatusSeeOther {
		t.Fatalf("anonymous total-days card: %d", rr.Code)
	}
	if _, err := s.shares.Create(context.Background(), owner, share.TargetDay, d.ID, []string{bob.Username}, false, homeNow); err != nil {
		t.Fatal(err)
	}
	acceptAllInvitations(t, s, bobToken, bob.ID)
	rr := get(s.Handler(), path, bobToken)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `<li><strong>62</strong> days</li>`) ||
		strings.Contains(rr.Body.String(), `class="dropdown day-actions"`) {
		t.Fatal("shared total-days card is missing or exposes owner controls")
	}
	if err := s.days.SetArchived(context.Background(), owner, d.ID, true, homeNow); err != nil {
		t.Fatal(err)
	}
	body := get(s.Handler(), path, token).Body.String()
	if !strings.Contains(body, "day-archived") || !strings.Contains(body, `<li><strong>62</strong> days</li>`) ||
		!strings.Contains(body, `>ago</p>`) {
		t.Error("archived total-days card lost its elapsed count or direction")
	}
}

package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"pandadays/internal/days"
)

func TestArchiveActions(t *testing.T) {
	s, owner, token := homeServer(t)
	d := mustDay(t, s, owner, days.DayInput{Title: "Trip", Date: "2027-01-01"})
	path := dayPath(d.ID)
	for _, action := range []string{"archive", "unarchive"} {
		body := get(s.Handler(), path, token).Body.String()
		if !strings.Contains(body, `action="`+path+`/`+action+`"`) ||
			!strings.Contains(body, `hx-post="`+path+`/`+action+`"`) {
			t.Fatalf("card missing %s action", action)
		}
		rr := sessionPost(t, s, path+"/"+action, token, nil)
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != path {
			t.Fatalf("%s: %d %q", action, rr.Code, rr.Header().Get("Location"))
		}
		got, err := s.days.Get(context.Background(), owner, d.ID)
		if err != nil || (got.ArchivedAt != nil) != (action == "archive") {
			t.Fatalf("%s state: %+v %v", action, got, err)
		}
		if got.ArchivedAt != nil && !got.ArchivedAt.Equal(homeNow) {
			t.Fatalf("archive timestamp = %v, want %v", got.ArchivedAt, homeNow)
		}
		if titles := listedTitles(get(s.Handler(), "/", token).Body.String()); (len(titles) == 0) != (action == "archive") {
			t.Fatalf("%s Home visibility: %v", action, titles)
		}
		if titles := listedTitles(get(s.Handler(), "/archive", token).Body.String()); (len(titles) == 1) != (action == "archive") {
			t.Fatalf("%s Archive visibility: %v", action, titles)
		}
	}
	for _, action := range []string{"archive", "unarchive"} {
		r := sessionRequest("POST", path+"/"+action, token)
		r.Header.Set("HX-Request", "true")
		r.Header.Set("X-CSRF-Token", sessionCSRF(t, s, token))
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, r)
		if rr.Code != http.StatusOK || rr.Header().Get("HX-Redirect") != path {
			t.Fatalf("htmx %s: %d %q", action, rr.Code, rr.Header().Get("HX-Redirect"))
		}
	}
}

func TestArchiveActionsAuthorizationAndCSRF(t *testing.T) {
	s, owner, token := homeServer(t)
	d := mustDay(t, s, owner, days.DayInput{Title: "Mine", Date: "2027-01-01"})
	_, otherToken := secondUser(t, s)
	for _, action := range []string{"archive", "unarchive"} {
		path := dayPath(d.ID) + "/" + action
		for _, tc := range []struct {
			path, token string
		}{
			{path, otherToken},
			{"/days/99999/" + action, token},
			{"/days/nope/" + action, token},
			{"/days/0/" + action, token},
		} {
			if rr := sessionPost(t, s, tc.path, tc.token, nil); rr.Code != http.StatusNotFound {
				t.Errorf("POST %s: %d, want 404", tc.path, rr.Code)
			}
		}
		r := sessionRequest("POST", path, token)
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, r)
		if rr.Code != http.StatusForbidden {
			t.Errorf("POST %s without CSRF: %d", path, rr.Code)
		}
		if rr := get(s.Handler(), path, token); rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: %d", path, rr.Code)
		}
		r = sessionRequest("POST", path, "")
		login := get(s.Handler(), "/login", "")
		var anonymousCSRF string
		for _, cookie := range login.Result().Cookies() {
			if cookie.Name == csrfCookieName {
				r.AddCookie(cookie)
				anonymousCSRF = cookie.Value
			}
		}
		if anonymousCSRF == "" {
			t.Fatal("login did not issue an anonymous CSRF cookie")
		}
		r.Header.Set("X-CSRF-Token", anonymousCSRF)
		rr = httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, r)
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
			t.Errorf("anonymous POST %s: %d %q", path, rr.Code, rr.Header().Get("Location"))
		}
	}
	got, err := s.days.Get(context.Background(), owner, d.ID)
	if err != nil || !reflect.DeepEqual(got, d) {
		t.Fatalf("denied requests changed day: %+v %v", got, err)
	}
	if rr := get(s.Handler(), "/archive", ""); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous archive: %d", rr.Code)
	}
}

func TestArchiveAndGroupPageVisibility(t *testing.T) {
	s, owner, token := homeServer(t)
	ctx := context.Background()
	col, err := s.collections.Create(ctx, owner, days.GroupInput{Name: "Family"}, homeNow)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := s.tags.Create(ctx, owner, days.GroupInput{Name: "Special"}, homeNow)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		title, date string
		repeat      days.Repeat
		archived    bool
	}{
		{"Upcoming", "2026-10-20", days.RepeatNone, false},
		{"Past", "2026-10-17", days.RepeatNone, false},
		{"Archived future", "2026-10-21", days.RepeatNone, true},
		{"Archived past", "2026-10-16", days.RepeatNone, true},
		{"Archived repeat", "2000-10-22", days.RepeatYearly, true},
		{"Archived today", "2026-10-18", days.RepeatNone, true},
	} {
		d := mustDay(t, s, owner, days.DayInput{Title: tc.title, Date: tc.date, Repeat: tc.repeat,
			CollectionID: col.ID, TagIDs: []int64{tag.ID}, Comment: "private comment", StyleColor: "#abcdef", StyleIcon: "*"})
		if tc.archived {
			if err := s.days.SetArchived(ctx, owner, d.ID, true, homeNow); err != nil {
				t.Fatal(err)
			}
		}
	}
	mustDay(t, s, owner, days.DayInput{Title: "Elsewhere", Date: "2027-01-01"})
	bob, bobToken := secondUser(t, s)
	foreign := mustDay(t, s, bob.ID, days.DayInput{Title: "Foreign", Date: "2027-01-01"})
	if err := s.days.SetArchived(ctx, bob.ID, foreign.ID, true, homeNow); err != nil {
		t.Fatal(err)
	}
	colPath := "/collections/" + strconv.FormatInt(col.ID, 10)
	tagPath := "/tags/" + strconv.FormatInt(tag.ID, 10)
	all := []string{"Archived today", "Upcoming", "Archived future", "Archived repeat", "Past", "Archived past"}
	archived := []string{"Archived today", "Archived future", "Archived repeat", "Archived past"}
	for _, tc := range []struct {
		path string
		want []string
	}{
		{"/", []string{"Upcoming", "Elsewhere"}},
		{"/?when=past", []string{"Past"}},
		{"/archive", archived},
		{colPath, all},
		{tagPath, all},
		{colPath + "?hide_archived=1", []string{"Upcoming", "Past"}},
		{tagPath + "?hide_archived=1", []string{"Upcoming", "Past"}},
		{colPath + "?hide_archived=0", all},
		{tagPath + "?hide_archived=bogus", all},
	} {
		rr := get(s.Handler(), tc.path, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", tc.path, rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		if got := listedTitles(body); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v; want %v", tc.path, got, tc.want)
		}
		if strings.Contains(body, "private comment") {
			t.Errorf("%s exposes comment in list", tc.path)
		}
		if strings.Contains(body, "Archived future</span>") {
			for _, want := range []string{`day-future day-archived`, `day-past day-archived`,
				`day-today day-archived`, `badge badge-archived`, `fill="#abcdef"`, `Special</span>`, `Family</span>`} {
				if !strings.Contains(body, want) {
					t.Errorf("%s missing visual state %q", tc.path, want)
				}
			}
		}
	}
	for _, path := range []string{colPath, tagPath} {
		for _, query := range []string{"", "?hide_archived=1"} {
			if rr := get(s.Handler(), path+query, bobToken); rr.Code != http.StatusNotFound {
				t.Errorf("foreign group %s%s: %d", path, query, rr.Code)
			}
		}
	}
}

func TestGroupArchiveFilterPartialAndEmptyStates(t *testing.T) {
	s, owner, token := homeServer(t)
	d := mustDay(t, s, owner, days.DayInput{Title: "Only archived", Date: "2026-10-17"})
	ctx := context.Background()
	if err := s.days.SetArchived(ctx, owner, d.ID, true, homeNow); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/collections/" + strconv.FormatInt(d.CollectionID, 10),
		"/tags/" + strconv.FormatInt(d.TagIDs[0], 10),
	} {
		full := get(s.Handler(), path, token).Body.String()
		if !strings.Contains(full, `hx-get="`+path+`?hide_archived=1"`) || !strings.Contains(full, "Hide archived") {
			t.Fatalf("missing hide toggle: %s", path)
		}
		rr := htmxGet(s, path+"?hide_archived=1", token, nil)
		body := rr.Body.String()
		if rr.Code != http.StatusOK || !strings.HasPrefix(body, `<div id="group-days"`) || strings.Contains(body, "<html") ||
			len(listedTitles(body)) != 0 || !strings.Contains(body, "No days match this filter.") ||
			!strings.Contains(body, "Show archived") || !strings.Contains(body, `hx-get="`+path+`"`) {
			t.Fatalf("filtered partial %s: %d %s", path, rr.Code, body)
		}
		if vary := strings.Join(rr.Header().Values("Vary"), ","); !strings.Contains(vary, "HX-Request") {
			t.Errorf("Vary = %q", vary)
		}
		shown := htmxGet(s, path, token, nil)
		if got := listedTitles(shown.Body.String()); !reflect.DeepEqual(got, []string{"Only archived"}) {
			t.Errorf("show archived: %v", got)
		}
		restore := htmxGet(s, path+"?hide_archived=1", token, map[string]string{"HX-History-Restore-Request": "true"})
		if !strings.Contains(restore.Body.String(), "<html") || len(listedTitles(restore.Body.String())) != 0 {
			t.Error("history restore did not return the filtered full page")
		}
	}
	if err := s.days.SetArchived(ctx, owner, d.ID, false, homeNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if rr := get(s.Handler(), "/archive", token); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "No archived days yet.") {
		t.Fatalf("archive empty state: %d %s", rr.Code, rr.Body.String())
	}
	// Validation errors must still render the full group page, including its list.
	path := "/collections/" + strconv.FormatInt(d.CollectionID, 10)
	rr := sessionPost(t, s, path+"/edit", token, url.Values{"name": {"Renamed"}, "color": {"invalid"}})
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "<html") ||
		!reflect.DeepEqual(listedTitles(rr.Body.String()), []string{"Only archived"}) {
		t.Fatalf("group validation: %d %s", rr.Code, rr.Body.String())
	}
}

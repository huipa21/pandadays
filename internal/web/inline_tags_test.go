package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestDayInlineTagFormSubmissionAndValidation(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	form := dayValues("title", "")
	form["new_tags"] = []string{"Family trip", "<b>literal</b>"}
	form.Set("tag_query", "#工作")
	rr := sessionPost(t, s, "/days", token, form)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid day: %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{`name="new_tags" value="Family trip"`, `value="&lt;b&gt;literal&lt;/b&gt;"`, `>#工作</textarea>`} {
		if !strings.Contains(body, want) {
			t.Errorf("validation lost %q", want)
		}
	}
	form.Set("title", "New day")
	rr = sessionPost(t, s, "/days", token, form)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	d := onlyDay(t, s, u.ID)
	if len(d.TagIDs) != 3 {
		t.Fatalf("tags=%v", d.TagIDs)
	}
	for _, id := range d.TagIDs {
		g, err := s.tags.Get(context.Background(), u.ID, id)
		if err != nil || g.DayCount != 1 {
			t.Fatalf("tag=%+v %v", g, err)
		}
	}
	edit := url.Values{"title": {"Edited"}, "date": {"2026-05-01"}, "tag_query": {"#family trip #Updated"}}
	rr = sessionPost(t, s, dayPath(d.ID)+"/edit", token, edit)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("edit: %d %s", rr.Code, rr.Body.String())
	}
	groups, err := s.tags.List(context.Background(), u.ID)
	if err != nil || len(groups) != 5 {
		t.Fatalf("case-insensitive reuse: %+v %v", groups, err)
	}
}

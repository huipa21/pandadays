package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"pandadays/internal/days"
	"pandadays/internal/share"
)

func TestHeaderSearchMatchesTitlesAcrossDates(t *testing.T) {
	s, owner, token := homeServer(t)
	for _, input := range []days.DayInput{
		{Title: "Future birthday", Date: "2027-01-01"},
		{Title: "Past BIRTHDAY", Date: "2025-01-01"},
		{Title: "生日聚会", Date: "2027-02-01"},
		{Title: "Literal 100%_done", Date: "2027-03-01"},
		{Title: "Other", Date: "2027-04-01", Comment: "birthday"},
	} {
		mustDay(t, s, owner, input)
	}
	archived := mustDay(t, s, owner, days.DayInput{Title: "Archived birthday", Date: "2027-01-01"})
	if err := s.days.SetArchived(context.Background(), owner, archived.ID, true, s.now()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		want  string
	}{
		{"  BiRtHdAy  ", "Future birthday|Past BIRTHDAY"},
		{"生日", "生日聚会"},
		{"%_", "Literal 100%_done"},
		{"no match", ""},
		{"<script>alert(1)</script>", ""},
	} {
		t.Run(tc.query, func(t *testing.T) {
			path := "/?q=" + url.QueryEscape(tc.query)
			for _, rr := range []*httptest.ResponseRecorder{
				get(s.Handler(), path, token),
				htmxGet(s, path, token, nil),
			} {
				if rr.Code != http.StatusOK {
					t.Fatalf("search status = %d: %s", rr.Code, rr.Body.String())
				}
				if got := strings.Join(listedTitles(rr.Body.String()), "|"); got != tc.want {
					t.Fatalf("search %q = %q, want %q", tc.query, got, tc.want)
				}
				if strings.Contains(rr.Body.String(), "<script>alert(1)</script>") {
					t.Fatal("query rendered as HTML")
				}
				if tc.want == "" && !strings.Contains(rr.Body.String(), "No matching days found.") {
					t.Fatal("missing empty search state")
				}
			}
		})
	}
	for _, path := range []string{"/", "/collections", "/shared", "/messages"} {
		rr := get(s.Handler(), path, token)
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `class="header-search" role="search"`) ||
			!strings.Contains(rr.Body.String(), `type="search" name="q"`) {
			t.Fatalf("missing header search on %s: %d", path, rr.Code)
		}
	}
	body := get(s.Handler(), "/?q=%20%20", token).Body.String()
	if !strings.Contains(body, `class="segmented"`) || strings.Contains(body, "Past BIRTHDAY") {
		t.Fatal("blank query did not restore normal Home")
	}
}

func TestSearchIncludesOnlyAuthorizedVisibleShares(t *testing.T) {
	s, owner, token := homeServer(t)
	bob, _ := secondUser(t, s)
	ctx := context.Background()
	for _, state := range []string{"accepted", "pending", "rejected", "hidden", "private"} {
		day := mustDay(t, s, bob.ID, days.DayInput{Title: "Match " + state, Date: "2027-01-01", Comment: "private comment"})
		if state == "private" {
			continue
		}
		sh, err := s.shares.Create(ctx, bob.ID, share.TargetDay, day.ID, []string{"Alice"}, false, s.now())
		if err != nil {
			t.Fatal(err)
		}
		if state != "pending" {
			if err := s.shares.Respond(ctx, owner, sh.ID, state != "rejected"); err != nil {
				t.Fatal(err)
			}
		}
		if state == "hidden" {
			if err := s.shares.SetHidden(ctx, owner, day.ID, true); err != nil {
				t.Fatal(err)
			}
		}
		if state == "accepted" {
			duplicate, err := s.shares.Create(ctx, bob.ID, share.TargetDay, day.ID, []string{"Alice"}, false, s.now())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.shares.Respond(ctx, owner, duplicate.ID, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	rr := get(s.Handler(), "/?q=match", token)
	if rr.Code != http.StatusOK || strings.Join(listedTitles(rr.Body.String()), "|") != "Match accepted" {
		t.Fatalf("search access boundary: %d, %v", rr.Code, listedTitles(rr.Body.String()))
	}
	if !strings.Contains(rr.Body.String(), "From @bob1") || strings.Contains(rr.Body.String(), "private comment") {
		t.Fatal("search lost shared metadata or exposed private comments")
	}
}

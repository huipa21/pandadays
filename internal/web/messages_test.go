package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"pandadays/internal/days"
	"pandadays/internal/media"
	"pandadays/internal/share"
)

func acceptAllInvitations(t *testing.T, s *Server, token string, userID int64) {
	t.Helper()
	items, _, err := s.shares.Inbox(context.Background(), userID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		rr := sessionPost(t, s, "/messages/"+strconv.FormatInt(item.ShareID, 10)+"/accept", token, nil)
		if rr.Code != http.StatusSeeOther {
			t.Fatalf("accept invitation: %d %s", rr.Code, rr.Body.String())
		}
	}
}

func TestMessagesBadgeDecisionsAndAuthorization(t *testing.T) {
	s, _, owner, ownerToken := authenticatedServer(t)
	s.media = media.New(s.db, filepath.Join(t.TempDir(), "media"))
	bob, bobToken := secondUser(t, s)
	ctx := context.Background()
	day, err := s.days.Create(ctx, owner.ID, days.DayInput{Title: "Invitation title", Date: "2027-01-01"}, s.now())
	if err != nil {
		t.Fatal(err)
	}
	if rr := uploadAttachment(t, s, ownerToken, day.ID, "invitation.txt", []byte("shared file")); rr.Code != http.StatusSeeOther {
		t.Fatalf("upload = %d", rr.Code)
	}
	files, err := s.media.ListDay(ctx, owner.ID, day.ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("attachments = %+v, %v", files, err)
	}
	filePath := "/attachments/" + strconv.FormatInt(files[0].ID, 10)
	sh, err := s.shares.Create(ctx, owner.ID, share.TargetDay, day.ID, []string{"Bob"}, true, s.now())
	if err != nil {
		t.Fatal(err)
	}
	home := get(s.Handler(), "/", bobToken)
	if !strings.Contains(home.Body.String(), `class="notification-badge"`) || strings.Contains(home.Body.String(), day.Title) {
		t.Fatalf("pending home: %d %s", home.Code, home.Body.String())
	}
	if rr := get(s.Handler(), dayPath(day.ID), bobToken); rr.Code != http.StatusNotFound {
		t.Fatalf("pending detail = %d", rr.Code)
	}
	if rr := get(s.Handler(), filePath, bobToken); rr.Code != http.StatusNotFound {
		t.Fatalf("pending attachment = %d", rr.Code)
	}
	path := "/messages/" + strconv.FormatInt(sh.ID, 10)
	req := sessionRequest(http.MethodPost, path+"/accept", bobToken)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("response without CSRF = %d", rr.Code)
	}
	if rr := sessionPost(t, s, path+"/accept", ownerToken, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("owner accepted recipient invitation: %d", rr.Code)
	}
	for _, action := range []string{"reject", "accept"} {
		rr := sessionPost(t, s, path+"/"+action, bobToken, nil)
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/messages" {
			t.Fatalf("response %s: %d %s", action, rr.Code, rr.Body.String())
		}
		inbox := get(s.Handler(), "/messages", bobToken)
		if inbox.Code != http.StatusOK || !strings.Contains(inbox.Body.String(), day.Title) ||
			strings.Contains(inbox.Body.String(), `class="notification-badge"`) {
			t.Fatalf("inbox after %s: %d %s", action, inbox.Code, inbox.Body.String())
		}
		if action == "reject" && !strings.Contains(inbox.Body.String(), path+"/accept") {
			t.Fatal("rejected invitation cannot be accepted")
		}
		want := http.StatusNotFound
		if action == "accept" {
			want = http.StatusOK
		}
		if rr := get(s.Handler(), filePath, bobToken); rr.Code != want {
			t.Fatalf("attachment after %s = %d, want %d", action, rr.Code, want)
		}
		accessible, err := s.media.ListAccessibleDay(ctx, bob.ID, day.ID)
		if err != nil || (len(accessible) != 0) != (action == "accept") {
			t.Fatalf("attachment list after %s = %+v, %v", action, accessible, err)
		}
	}

	if allowed, _, err := s.shares.CanView(ctx, bob.ID, day.ID); err != nil || !allowed {
		t.Fatalf("accepted access = %v, %v", allowed, err)
	}
	for _, raw := range []string{"-1", "0", "oops", "9223372036854775808"} {
		if rr := get(s.Handler(), "/messages?before="+raw, bobToken); rr.Code != http.StatusBadRequest {
			t.Errorf("cursor %q: %d", raw, rr.Code)
		}
	}
}

func TestMessageLoadMoreAndLogin(t *testing.T) {
	s, owner, token := homeServer(t)
	bob, bobToken := secondUser(t, s)
	day := mustDay(t, s, owner, days.DayInput{Title: "Queue item", Date: "2027-01-01"})
	ctx := context.Background()
	for i := 0; i < share.InboxPageSize+1; i++ {
		if _, err := s.shares.Create(ctx, owner, share.TargetDay, day.ID, []string{bob.Username}, false, s.now()); err != nil {
			t.Fatal(err)
		}
	}
	items, _, err := s.shares.Inbox(ctx, bob.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	first := get(s.Handler(), "/messages", bobToken)
	if first.Code != http.StatusOK || strings.Count(first.Body.String(), `<li class="card stack">`) != share.InboxPageSize ||
		!strings.Contains(first.Body.String(), `hx-target="closest li"`) {
		t.Fatalf("first page: %d %s", first.Code, first.Body.String())
	}
	nextURL := "/messages?before=" + strconv.FormatInt(items[len(items)-1].ShareID, 10)
	for _, rr := range []*httptest.ResponseRecorder{
		get(s.Handler(), nextURL, bobToken),
		htmxGet(s, nextURL, bobToken, nil),
	} {
		if rr.Code != http.StatusOK || strings.Count(rr.Body.String(), `<li class="card stack">`) != 1 ||
			strings.Contains(rr.Body.String(), `class="load-more"`) {
			t.Fatalf("next page: %d %s", rr.Code, rr.Body.String())
		}
	}
	if rr := htmxGet(s, nextURL, bobToken, nil); strings.Contains(rr.Body.String(), "<!doctype") {
		t.Fatal("load more returned full layout")
	}
	if body := get(s.Handler(), "/messages", token).Body.String(); strings.Contains(body, day.Title) {
		t.Fatal("sender inbox contains recipient messages")
	}
	if rr := get(s.Handler(), "/messages", ""); rr.Header().Get("Location") != "/login" {
		t.Fatalf("anonymous inbox = %d", rr.Code)
	}
}

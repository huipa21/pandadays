package web

import (
	"context"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/days"
	"pandadays/internal/media"
	"pandadays/internal/share"
)

func TestSharingFlowAndReadOnlyRecipientViews(t *testing.T) {
	s, _, owner, ownerToken := authenticatedServer(t)
	s.media = media.New(s.db, filepath.Join(t.TempDir(), "media"))
	bob, bobToken := secondUser(t, s)
	carol, err := s.auth.CreateUser(context.Background(), auth.CreateUserParams{
		Username: "carol", Email: "carol@example.com", Password: "password123",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	collection, err := s.collections.Create(context.Background(), owner.ID,
		days.GroupInput{Name: "Owner-private collection"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tag, err := s.tags.Create(context.Background(), owner.ID, days.GroupInput{Name: "Owner-private tag"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	day, err := s.days.Create(context.Background(), owner.ID, days.DayInput{
		Title: "Shared birthday", Date: "2027-02-01", CollectionID: collection.ID,
		TagIDs: []int64{tag.ID}, Comment: "Visible comment",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if uploaded := uploadAttachment(t, s, ownerToken, day.ID, "shared-note.txt", []byte("attachment data")); uploaded.Code != http.StatusSeeOther {
		t.Fatalf("upload attachment before sharing: %d %s", uploaded.Code, uploaded.Body.String())
	}

	path := dayPath(day.ID) + "/shares"
	create := sessionPost(t, s, path, ownerToken, url.Values{
		"recipients":   {"bob1, CAROL@example.com"},
		"show_comment": {"1"},
	})
	if create.Code != http.StatusSeeOther || create.Header().Get("Location") != dayPath(day.ID)+"#day-sharing" {
		t.Fatalf("create share: %d %q %s", create.Code, create.Header().Get("Location"), create.Body.String())
	}
	acceptAllInvitations(t, s, bobToken, bob.ID)

	home := get(s.Handler(), "/", bobToken)
	for _, want := range []string{"Shared birthday", "day-shared", "From @alice"} {
		if !strings.Contains(home.Body.String(), want) {
			t.Errorf("recipient Home missing %q", want)
		}
	}
	detail := get(s.Handler(), dayPath(day.ID), bobToken)
	body := detail.Body.String()
	for _, want := range []string{"Shared birthday", "Visible comment", "From @alice", "Attachments", "shared-note.txt"} {
		if !strings.Contains(body, want) {
			t.Errorf("shared card missing %q", want)
		}
	}
	for _, forbidden := range []string{"Owner-private collection", "Owner-private tag", dayPath(day.ID) + "/edit",
		dayPath(day.ID) + "/delete", `name="file"`, `name="show_comment"`, `day-menu-section`} {
		if strings.Contains(body, forbidden) {
			t.Errorf("shared card leaked or offered owner-only field %q", forbidden)
		}
	}
	if detail.Code != http.StatusOK {
		t.Fatalf("shared card status: %d", detail.Code)
	}

	hide := sessionPost(t, s, "/shared/"+strconv.FormatInt(day.ID, 10)+"/hide", bobToken, nil)
	if hide.Code != http.StatusSeeOther || hide.Header().Get("Location") != "/" {
		t.Fatalf("hide shared day: %d %q", hide.Code, hide.Header().Get("Location"))
	}
	if strings.Contains(get(s.Handler(), "/", bobToken).Body.String(), "Shared birthday") {
		t.Fatal("hidden shared day remained on Home")
	}
	sharedPage := get(s.Handler(), "/shared", bobToken)
	if !strings.Contains(sharedPage.Body.String(), "Shared birthday") ||
		!strings.Contains(sharedPage.Body.String(), "/shared/"+strconv.FormatInt(day.ID, 10)+"/unhide?from=shared") {
		t.Fatalf("hidden day missing from Shared with me: %d %s", sharedPage.Code, sharedPage.Body.String())
	}
	unhide := sessionPost(t, s, "/shared/"+strconv.FormatInt(day.ID, 10)+"/unhide?from=shared", bobToken, nil)
	if unhide.Code != http.StatusSeeOther || unhide.Header().Get("Location") != "/shared" {
		t.Fatalf("unhide shared day: %d %q", unhide.Code, unhide.Header().Get("Location"))
	}

	shares, err := s.shares.List(context.Background(), owner.ID, share.TargetDay, day.ID)
	if err != nil || len(shares) != 1 || len(shares[0].Recipients) != 2 {
		t.Fatalf("owner share list: %+v, %v", shares, err)
	}
	update := sessionPost(t, s, "/shares/"+strconv.FormatInt(shares[0].ID, 10)+"/edit",
		ownerToken, url.Values{"recipients": {"bob1"}})
	if update.Code != http.StatusSeeOther {
		t.Fatalf("update share: %d %s", update.Code, update.Body.String())
	}
	if body := get(s.Handler(), dayPath(day.ID), bobToken).Body.String(); strings.Contains(body, "Visible comment") {
		t.Fatal("comment remained visible after owner disabled it")
	}
	if rr := get(s.Handler(), dayPath(day.ID), tokenFor(t, s, carol.ID)); rr.Code != http.StatusNotFound {
		t.Fatalf("removed recipient still has shared access: %d", rr.Code)
	}
	shares, err = s.shares.List(context.Background(), owner.ID, share.TargetDay, day.ID)
	if err != nil || len(shares) != 1 || len(shares[0].Recipients) != 1 {
		t.Fatalf("updated share recipients: %+v, %v", shares, err)
	}
	if rr := sessionPost(t, s, "/shares/"+strconv.FormatInt(shares[0].ID, 10)+"/revoke", ownerToken, nil); rr.Code != http.StatusSeeOther {
		t.Fatalf("revoke share: %d %s", rr.Code, rr.Body.String())
	}
	for _, recipient := range []*auth.User{bob, carol} {
		if rr := get(s.Handler(), dayPath(day.ID), tokenFor(t, s, recipient.ID)); rr.Code != http.StatusNotFound {
			t.Errorf("revoked day for %s: %d", recipient.Username, rr.Code)
		}
	}
}

func TestCollectionSharingIsLiveAndHidesArchivedDays(t *testing.T) {
	s, _, owner, ownerToken := authenticatedServer(t)
	bob, bobToken := secondUser(t, s)
	collection, err := s.collections.Create(context.Background(), owner.ID, days.GroupInput{Name: "Live"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.days.Create(context.Background(), owner.ID, days.DayInput{
		Title: "First shared item", Date: "2027-01-01", CollectionID: collection.ID, Comment: "private note",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	shareResponse := sessionPost(t, s, "/collections/"+strconv.FormatInt(collection.ID, 10)+"/shares",
		ownerToken, url.Values{"recipients": {"bob1"}})
	if shareResponse.Code != http.StatusSeeOther {
		t.Fatalf("create collection share: %d %s", shareResponse.Code, shareResponse.Body.String())
	}
	acceptAllInvitations(t, s, bobToken, bob.ID)
	second, err := s.days.Create(context.Background(), owner.ID, days.DayInput{
		Title: "Added after share", Date: "2027-02-01", CollectionID: collection.ID,
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.days.SetArchived(context.Background(), owner.ID, first.ID, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	list := get(s.Handler(), "/shared", bobToken)
	body := list.Body.String()
	if list.Code != http.StatusOK || strings.Contains(body, "First shared item") ||
		!strings.Contains(body, "Added after share") {
		t.Fatalf("collection share not live or exposed archived day: %d %s", list.Code, body)
	}
	if rr := get(s.Handler(), dayPath(first.ID), bobToken); rr.Code != http.StatusNotFound {
		t.Errorf("archived collection day: %d", rr.Code)
	}
	detail := get(s.Handler(), dayPath(second.ID), bobToken)
	if detail.Code != http.StatusOK || strings.Contains(detail.Body.String(), "private note") ||
		strings.Contains(detail.Body.String(), "Live") || strings.Contains(detail.Body.String(), dayPath(second.ID)+"/edit") {
		t.Fatalf("collection shared card leaked data: %d %s", detail.Code, detail.Body.String())
	}
}

func TestSharingErrorsDoNotRevealUnknownAccounts(t *testing.T) {
	s, _, owner, ownerToken := authenticatedServer(t)
	day, err := s.days.Create(context.Background(), owner.ID, days.DayInput{Title: "Share", Date: "2027-01-01"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := dayPath(day.ID) + "/shares"
	unknownA := sessionPost(t, s, path, ownerToken, url.Values{"recipients": {"not-a-user"}})
	unknownB := sessionPost(t, s, path, ownerToken, url.Values{"recipients": {"nobody@example.com"}})
	if unknownA.Code != http.StatusUnprocessableEntity || unknownB.Code != unknownA.Code ||
		unknownA.Body.String() != unknownB.Body.String() {
		t.Fatalf("unknown recipient responses differ: %d %q vs %d %q",
			unknownA.Code, unknownA.Body.String(), unknownB.Code, unknownB.Body.String())
	}
	self := sessionPost(t, s, path, ownerToken, url.Values{"recipients": {"ALICE@example.com"}})
	if self.Code != http.StatusUnprocessableEntity || !strings.Contains(self.Body.String(), "cannot share with yourself") {
		t.Fatalf("self share response: %d %q", self.Code, self.Body.String())
	}
	if rr := sessionPost(t, s, path, ownerToken, url.Values{"recipients": {"Alice"}}); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("self username share: %d", rr.Code)
	}
}

func tokenFor(t *testing.T, s *Server, userID int64) string {
	t.Helper()
	token, _, err := s.auth.CreateSession(context.Background(), userID, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return token
}

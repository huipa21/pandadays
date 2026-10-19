package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/days"
)

func sessionPost(t *testing.T, s *Server, path, token string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf_token", sessionCSRF(t, s, token))
	return postForm(s.Handler(), path, form, &http.Cookie{Name: auth.SessionCookieName, Value: token})
}

func secondUser(t *testing.T, s *Server) (*auth.User, string) {
	t.Helper()
	u, err := s.auth.CreateUser(context.Background(), auth.CreateUserParams{
		Username: "Bob", Email: "bob@example.com", Password: "password123",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := s.auth.CreateSession(context.Background(), u.ID, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return u, token
}

func TestGroupPagesRequireLogin(t *testing.T) {
	s, _, _, _ := authenticatedServer(t)
	h := s.Handler()
	for _, path := range []string{"/collections", "/collections/1", "/tags", "/tags/1"} {
		if rr := get(h, path, ""); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
			t.Errorf("GET %s anonymous: %d %q", path, rr.Code, rr.Header().Get("Location"))
		}
	}
}

func TestGroupCreationPopupsAndCollectionCards(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	for _, base := range []string{"/tags", "/collections"} {
		body := get(s.Handler(), base, token).Body.String()
		for _, want := range []string{`data-open-dialog="group-create"`, `<dialog id="group-create"`, `aria-labelledby="group-create-title"`} {
			if !strings.Contains(body, want) {
				t.Errorf("%s missing %q", base, want)
			}
		}
		if strings.Contains(body, ` open data-auto-open`) {
			t.Errorf("%s opens create popup on ordinary GET", base)
		}
		body = get(s.Handler(), base+"?create=1", token).Body.String()
		if !strings.Contains(body, ` open data-auto-open`) {
			t.Errorf("%s missing no-JS create popup", base)
		}
	}
	rr := sessionPost(t, s, "/collections", token, url.Values{
		"name": {"Notebook"}, "color": {"#aabbcc"}, "comment": {"Family\n<script>literal</script>"},
	})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	list, err := s.collections.List(context.Background(), u.ID)
	if err != nil || len(list) != 2 || list[1].Comment != "Family\n<script>literal</script>" {
		t.Fatalf("saved: %+v %v", list, err)
	}
	body := get(s.Handler(), "/collections", token).Body.String()
	for _, want := range []string{`collection-card`, `collection-cover`, `fill="#aabbcc"`, `Family`, `&lt;script&gt;literal&lt;/script&gt;`, `0 days`} {
		if !strings.Contains(body, want) {
			t.Errorf("card missing %q", want)
		}
	}
	rr = sessionPost(t, s, "/collections", token, url.Values{
		"name": {"Notebook"}, "color": {"#123456"}, "comment": {"Retained comment"},
	})
	body = rr.Body.String()
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(body, ` open data-auto-open`) ||
		!strings.Contains(body, "Retained comment") {
		t.Fatalf("validation popup: %d %s", rr.Code, body)
	}
	body = get(s.Handler(), "/tags", token).Body.String()
	if !strings.Contains(body, `tag-list`) || !strings.Contains(body, `tag-item`) || strings.Contains(body, `name="comment"`) {
		t.Fatal("tag list should show compact tags, without collection comment field")
	}
}

func TestGroupCRUDFlow(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	h := s.Handler()
	for _, tc := range []struct {
		base  string
		store *days.Groups
	}{{"/collections", s.collections}, {"/tags", s.tags}} {
		rr := get(h, tc.base, token)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), "Default") ||
			!strings.Contains(rr.Body.String(), `action="`+tc.base+`"`) {
			t.Fatalf("%s list: %d", tc.base, rr.Code)
		}

		rr = sessionPost(t, s, tc.base, token, url.Values{"name": {"Travel"}, "color": {"#3366FF"}})
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != tc.base {
			t.Fatalf("%s create: %d %s", tc.base, rr.Code, rr.Body.String())
		}
		groups, _ := tc.store.List(context.Background(), u.ID)
		if len(groups) != 2 || groups[1].Name != "Travel" || groups[1].Color != "#3366ff" {
			t.Fatalf("%s after create: %+v", tc.base, groups)
		}
		id := strconv.FormatInt(groups[1].ID, 10)
		if body := get(h, tc.base, token).Body.String(); !strings.Contains(body, "Travel") ||
			!strings.Contains(body, `fill="#3366ff"`) {
			t.Fatalf("%s list missing new group or swatch", tc.base)
		}

		rr = sessionPost(t, s, tc.base, token, url.Values{"name": {"travel"}, "color": {"#000000"}})
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "You already have one with this name.") {
			t.Fatalf("%s duplicate create: %d", tc.base, rr.Code)
		}
		rr = sessionPost(t, s, tc.base, token, url.Values{"name": {"default"}, "color": {"#000000"}})
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "This name is reserved.") {
			t.Fatalf("%s reserved-name create: %d", tc.base, rr.Code)
		}

		rr = sessionPost(t, s, tc.base+"/"+id+"/edit", token, url.Values{"name": {"Trips"}, "color": {"#000000"}, "no_color": {"1"}})
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != tc.base+"/"+id {
			t.Fatalf("%s rename: %d", tc.base, rr.Code)
		}
		if g, _ := tc.store.Get(context.Background(), u.ID, groups[1].ID); g.Name != "Trips" || g.Color != "" {
			t.Fatalf("%s after rename: %+v", tc.base, g)
		}
		if rr := get(h, tc.base+"/"+id, token); rr.Code != 200 || !strings.Contains(rr.Body.String(), "Trips") ||
			!strings.Contains(rr.Body.String(), "/delete") {
			t.Fatalf("%s detail: %d", tc.base, rr.Code)
		}

		rr = sessionPost(t, s, tc.base+"/"+id+"/delete", token, nil)
		if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != tc.base {
			t.Fatalf("%s delete: %d", tc.base, rr.Code)
		}
		if rr := get(h, tc.base+"/"+id, token); rr.Code != http.StatusNotFound {
			t.Fatalf("%s deleted detail: %d", tc.base, rr.Code)
		}
	}
}

func TestGroupDeleteHtmxRedirect(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	g, err := s.tags.Create(context.Background(), u.ID, days.GroupInput{Name: "Temp"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := sessionRequest("POST", "/tags/"+strconv.FormatInt(g.ID, 10)+"/delete", token)
	r.Header.Set("HX-Request", "true")
	r.Header.Set("X-CSRF-Token", sessionCSRF(t, s, token))
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	if rr.Code != 200 || rr.Header().Get("HX-Redirect") != "/tags" {
		t.Fatalf("htmx delete: %d %q", rr.Code, rr.Header().Get("HX-Redirect"))
	}
}

func TestGroupReservedProtectionHTTP(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	h := s.Handler()
	for _, tc := range []struct {
		base  string
		store *days.Groups
	}{{"/collections", s.collections}, {"/tags", s.tags}} {
		def, err := tc.store.DefaultID(context.Background(), u.ID)
		if err != nil {
			t.Fatal(err)
		}
		path := tc.base + "/" + strconv.FormatInt(def, 10)
		detail := get(h, path, token)
		if detail.Code != 200 || strings.Contains(detail.Body.String(), "/delete") ||
			strings.Contains(detail.Body.String(), `name="name"`) {
			t.Fatalf("%s reserved detail offers rename/delete: %d", tc.base, detail.Code)
		}
		if rr := sessionPost(t, s, path+"/edit", token, url.Values{"name": {"Mine"}, "color": {"#000000"}}); rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s rename reserved: %d", tc.base, rr.Code)
		}
		if rr := sessionPost(t, s, path+"/delete", token, nil); rr.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s delete reserved: %d", tc.base, rr.Code)
		}
		if rr := sessionPost(t, s, path+"/edit", token, url.Values{"color": {"#aa0000"}}); rr.Code != http.StatusSeeOther {
			t.Errorf("%s recolor reserved: %d", tc.base, rr.Code)
		}
		g, err := tc.store.Get(context.Background(), u.ID, def)
		if err != nil || g.Name != "Default" || g.Color != "#aa0000" {
			t.Fatalf("%s reserved after requests: %+v %v", tc.base, g, err)
		}
	}
}

func TestGroupCrossUserAccess404(t *testing.T) {
	s, _, alice, _ := authenticatedServer(t)
	_, bobToken := secondUser(t, s)
	h := s.Handler()
	for _, tc := range []struct {
		base  string
		store *days.Groups
	}{{"/collections", s.collections}, {"/tags", s.tags}} {
		g, err := tc.store.Create(context.Background(), alice.ID, days.GroupInput{Name: "Private"}, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		def, _ := tc.store.DefaultID(context.Background(), alice.ID)
		for _, id := range []int64{g.ID, def} {
			path := tc.base + "/" + strconv.FormatInt(id, 10)
			if rr := get(h, path, bobToken); rr.Code != http.StatusNotFound {
				t.Errorf("GET %s as bob: %d", path, rr.Code)
			}
			if rr := sessionPost(t, s, path+"/edit", bobToken, url.Values{"name": {"Stolen"}, "color": {"#000000"}}); rr.Code != http.StatusNotFound {
				t.Errorf("edit %s as bob: %d", path, rr.Code)
			}
			if rr := sessionPost(t, s, path+"/delete", bobToken, nil); rr.Code != http.StatusNotFound {
				t.Errorf("delete %s as bob: %d", path, rr.Code)
			}
		}
		if body := get(h, tc.base, bobToken).Body.String(); strings.Contains(body, "Private") {
			t.Errorf("%s list leaks another user's group", tc.base)
		}
		if got, err := tc.store.Get(context.Background(), alice.ID, g.ID); err != nil || got.Name != "Private" {
			t.Fatalf("%s alice's group changed: %+v %v", tc.base, got, err)
		}
	}
	for _, path := range []string{"/collections/abc", "/tags/0", "/tags/-1"} {
		if rr := get(h, path, bobToken); rr.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d", path, rr.Code)
		}
	}
}

func TestGroupDeleteRequiresCSRF(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	g, _ := s.collections.Create(context.Background(), u.ID, days.GroupInput{Name: "Keep"}, time.Now())
	rr := postForm(s.Handler(), "/collections/"+strconv.FormatInt(g.ID, 10)+"/delete", url.Values{},
		&http.Cookie{Name: auth.SessionCookieName, Value: token})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("delete without CSRF: %d", rr.Code)
	}
	if _, err := s.collections.Get(context.Background(), u.ID, g.ID); err != nil {
		t.Fatalf("collection deleted without CSRF: %v", err)
	}
}

func TestGroupPagesLocalized(t *testing.T) {
	s, db, u, token := authenticatedServer(t)
	if _, err := db.Exec(`UPDATE users SET lang = 'zh' WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	body := get(s.Handler(), "/collections", token).Body.String()
	if !strings.Contains(body, "默认") || !strings.Contains(body, "新建合集") {
		t.Fatal("collections page not localized")
	}
}

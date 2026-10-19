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

	"pandadays/internal/auth"
	"pandadays/internal/days"
)

func dayValues(kv ...string) url.Values {
	v := url.Values{"title": {"Trip"}, "date": {"2026-05-01"}, "repeat": {"none"}}
	for i := 0; i+1 < len(kv); i += 2 {
		v[kv[i]] = append(v[kv[i]][:0:0], kv[i+1])
	}
	return v
}

func onlyDay(t *testing.T, s *Server, owner int64) days.Day {
	t.Helper()
	var id int64
	if err := s.db.QueryRow(`SELECT id FROM days WHERE owner_id = ?`, owner).Scan(&id); err != nil {
		t.Fatal(err)
	}
	d, err := s.days.Get(context.Background(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDayPagesRequireLogin(t *testing.T) {
	s, _, _, _ := authenticatedServer(t)
	for _, path := range []string{"/days/new", "/days/1", "/days/1/edit"} {
		if rr := get(s.Handler(), path, ""); rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != "/login" {
			t.Errorf("GET %s anonymous: %d %q", path, rr.Code, rr.Header().Get("Location"))
		}
	}
}

func TestDayNewFormDefaults(t *testing.T) {
	s, db, u, token := authenticatedServer(t)
	if _, err := db.Exec(`UPDATE users SET timezone = 'Pacific/Kiritimati' WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	defCol, _ := s.collections.DefaultID(ctx, u.ID)
	defTag, _ := s.tags.DefaultID(ctx, u.ID)
	other, _ := s.tags.Create(ctx, u.ID, days.GroupInput{Name: "Fun"}, time.Now())
	rr := get(s.Handler(), "/days/new", token)
	body := rr.Body.String()
	today := time.Now().In(userLocation(&auth.User{Timezone: "Pacific/Kiritimati"})).Format(time.DateOnly)
	for _, want := range []string{
		`action="/days"`,
		`data-day-form data-unsaved="false"`,
		`name="date" value="` + today + `"`,
		`<option value="none" selected>Does not repeat</option>`,
		`<option value="` + strconv.FormatInt(defCol, 10) + `" selected>Default</option>`,
		`name="tags" value="` + strconv.FormatInt(defTag, 10) + `" checked>`,
		`name="tags" value="` + strconv.FormatInt(other.ID, 10) + `">`,
		`name="no_color" value="1" checked>`,
		`data-tag-picker data-remove-label="Remove tag"`,
		`<textarea id="tag-query" name="tag_query" rows="2"`,
		`aria-controls="tag-suggestions"`,
		`data-tag-fallback class="chips"`,
		`data-icon-options role="group" aria-label="Choose an icon" hidden`,
		`data-color-presets hidden`,
		`data-custom-color title="Custom color"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("new form missing %q", want)
		}
	}
	if rr.Code != 200 || strings.Contains(body, "/delete") {
		t.Fatalf("new form: %d", rr.Code)
	}
}

func TestDayCreateAppliesDefaults(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	ctx := context.Background()
	rr := sessionPost(t, s, "/days", token, url.Values{"title": {"Trip"}, "date": {"2026-05-01"},
		"color": {"#123456"}, "no_color": {"1"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	d := onlyDay(t, s, u.ID)
	if rr.Header().Get("Location") != dayPath(d.ID) {
		t.Fatalf("redirect: %q", rr.Header().Get("Location"))
	}
	defCol, _ := s.collections.DefaultID(ctx, u.ID)
	defTag, _ := s.tags.DefaultID(ctx, u.ID)
	if d.Repeat != days.RepeatNone || d.CollectionID != defCol || !reflect.DeepEqual(d.TagIDs, []int64{defTag}) ||
		d.Time != "" || d.StyleColor != "" {
		t.Fatalf("defaults not applied: %+v", d)
	}
	view := get(s.Handler(), dayPath(d.ID), token)
	if view.Code != 200 || !strings.Contains(view.Body.String(), "Trip") || !strings.Contains(view.Body.String(), "All day") ||
		!strings.Contains(view.Body.String(), "/collections/"+strconv.FormatInt(defCol, 10)) {
		t.Fatalf("view: %d", view.Code)
	}
}

func TestDayCreateWithAllFields(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	ctx := context.Background()
	col, _ := s.collections.Create(ctx, u.ID, days.GroupInput{Name: "Family"}, time.Now())
	t1, _ := s.tags.Create(ctx, u.ID, days.GroupInput{Name: "Birthday"}, time.Now())
	t2, _ := s.tags.Create(ctx, u.ID, days.GroupInput{Name: "Mom"}, time.Now())
	form := url.Values{"title": {"Mom's birthday"}, "date": {"1960-03-15"}, "time": {"09:30"}, "repeat": {"yearly"},
		"collection": {strconv.FormatInt(col.ID, 10)}, "tags": {strconv.FormatInt(t1.ID, 10), strconv.FormatInt(t2.ID, 10)},
		"comment": {"<b>cake</b>"}, "color": {"#FF0000"}, "icon": {"🎂"}}
	if rr := sessionPost(t, s, "/days", token, form); rr.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	d := onlyDay(t, s, u.ID)
	if d.CollectionID != col.ID || !reflect.DeepEqual(d.TagIDs, []int64{t1.ID, t2.ID}) || d.Time != "09:30" ||
		d.Repeat != days.RepeatYearly || d.Comment != "<b>cake</b>" || d.StyleColor != "#ff0000" || d.StyleIcon != "🎂" {
		t.Fatalf("created: %+v", d)
	}
	body := get(s.Handler(), dayPath(d.ID), token).Body.String()
	if strings.Contains(body, "<b>cake</b>") || !strings.Contains(body, "&lt;b&gt;cake&lt;/b&gt;") ||
		!strings.Contains(body, "Every year") || !strings.Contains(body, "Birthday") || !strings.Contains(body, "Mom") {
		t.Fatal("view does not escape comment or misses fields")
	}
}

func TestDayValidationErrorsLocalized(t *testing.T) {
	s, db, u, token := authenticatedServer(t)
	for _, tc := range []struct {
		form url.Values
		want string
	}{
		{dayValues("title", " "), "Please enter a title."},
		{dayValues("title", strings.Repeat("a", 201)), "The title must be at most 200 characters."},
		{dayValues("date", "2026-02-30"), "Please enter a valid date."},
		{dayValues("time", "25:00"), "Please enter a valid time"},
		{dayValues("repeat", "daily"), "Please choose a valid repeat option."},
		{dayValues("comment", strings.Repeat("a", 10001)), "The comment must be at most 10,000 characters."},
		{dayValues("color", "blue"), "Please choose a valid color."},
		{dayValues("icon", "a b"), "The icon must be at most 16 characters"},
		{dayValues("collection", "abc"), "Please choose one of your collections."},
		{dayValues("tags", "999999"), "Please choose only your own tags."},
	} {
		rr := sessionPost(t, s, "/days", token, tc.form)
		if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), tc.want) {
			t.Errorf("%v: %d, want %q", tc.form, rr.Code, tc.want)
		}
	}
	// The form keeps the submitted values.
	rr := sessionPost(t, s, "/days", token, dayValues("title", "Keep me", "time", "25:00"))
	if !strings.Contains(rr.Body.String(), `value="Keep me"`) {
		t.Error("invalid form lost the submitted title")
	}
	if !strings.Contains(rr.Body.String(), `data-day-form data-unsaved="true"`) {
		t.Error("invalid form is not marked as unsaved")
	}
	if _, err := db.Exec(`UPDATE users SET lang = 'zh' WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if rr := sessionPost(t, s, "/days", token, dayValues("title", "")); !strings.Contains(rr.Body.String(), "请输入标题。") {
		t.Error("validation error not localized")
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM days`).Scan(&n)
	if n != 0 {
		t.Fatalf("invalid creates stored %d days", n)
	}
}

func TestDayQuotaHTTP(t *testing.T) {
	s, db, u, token := authenticatedServer(t)
	if _, err := db.Exec(`UPDATE users SET quota_days = 1 WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	if rr := sessionPost(t, s, "/days", token, dayValues()); rr.Code != http.StatusSeeOther {
		t.Fatalf("first create: %d", rr.Code)
	}
	rr := sessionPost(t, s, "/days", token, dayValues("title", "Second"))
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "maximum number of days") {
		t.Fatalf("over quota: %d", rr.Code)
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM days WHERE owner_id = ?`, u.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("days after quota: %d", n)
	}
}

func TestDayEditAndDelete(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	ctx := context.Background()
	d, err := s.days.Create(ctx, u.ID, days.DayInput{Title: "Trip", Date: "2026-05-01", StyleIcon: "✈"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	path := dayPath(d.ID)
	if rr := get(s.Handler(), path+"/edit", token); rr.Code != 200 || !strings.Contains(rr.Body.String(), `value="Trip"`) ||
		!strings.Contains(rr.Body.String(), `action="`+path+`/edit"`) || !strings.Contains(rr.Body.String(), path+"/delete") ||
		!strings.Contains(rr.Body.String(), `data-day-form data-unsaved="false"`) {
		t.Fatalf("edit form: %d", rr.Code)
	}
	rr := sessionPost(t, s, path+"/edit", token, dayValues("title", "Holiday", "repeat", "weekly", "time", "07:15"))
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != path {
		t.Fatalf("update: %d %s", rr.Code, rr.Body.String())
	}
	if got, _ := s.days.Get(ctx, u.ID, d.ID); got.Title != "Holiday" || got.Repeat != days.RepeatWeekly || got.Time != "07:15" || got.StyleIcon != "" {
		t.Fatalf("after update: %+v", got)
	}
	rr = sessionPost(t, s, path+"/edit", token, dayValues("date", "nope"))
	if rr.Code != http.StatusUnprocessableEntity || !strings.Contains(rr.Body.String(), "Please enter a valid date.") {
		t.Fatalf("invalid update: %d", rr.Code)
	}

	r := sessionRequest("POST", path+"/delete", token)
	r.Header.Set("HX-Request", "true")
	r.Header.Set("X-CSRF-Token", sessionCSRF(t, s, token))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("HX-Redirect") != "/" {
		t.Fatalf("htmx delete: %d %q", w.Code, w.Header().Get("HX-Redirect"))
	}
	if rr := get(s.Handler(), path, token); rr.Code != http.StatusNotFound {
		t.Fatalf("deleted day view: %d", rr.Code)
	}
}

func TestDayDeleteRequiresCSRF(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	d, _ := s.days.Create(context.Background(), u.ID, days.DayInput{Title: "Keep", Date: "2026-05-01"}, time.Now())
	rr := postForm(s.Handler(), dayPath(d.ID)+"/delete", url.Values{}, &http.Cookie{Name: auth.SessionCookieName, Value: token})
	if rr.Code != http.StatusForbidden {
		t.Fatalf("delete without CSRF: %d", rr.Code)
	}
	if _, err := s.days.Get(context.Background(), u.ID, d.ID); err != nil {
		t.Fatalf("day deleted without CSRF: %v", err)
	}
}

func TestDayCrossUserAccess404(t *testing.T) {
	s, _, alice, _ := authenticatedServer(t)
	bob, bobToken := secondUser(t, s)
	ctx := context.Background()
	d, _ := s.days.Create(ctx, alice.ID, days.DayInput{Title: "Secret", Date: "2026-05-01", Comment: "private"}, time.Now())
	path := dayPath(d.ID)
	for _, p := range []string{path, path + "/edit"} {
		if rr := get(s.Handler(), p, bobToken); rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "Secret") {
			t.Errorf("GET %s as bob: %d", p, rr.Code)
		}
	}
	if rr := sessionPost(t, s, path+"/edit", bobToken, dayValues("title", "Stolen")); rr.Code != http.StatusNotFound {
		t.Errorf("edit as bob: %d", rr.Code)
	}
	if rr := sessionPost(t, s, path+"/edit", bobToken, dayValues("title", "")); rr.Code != http.StatusNotFound {
		t.Errorf("invalid edit as bob: %d", rr.Code)
	}
	if rr := sessionPost(t, s, path+"/delete", bobToken, nil); rr.Code != http.StatusNotFound {
		t.Errorf("delete as bob: %d", rr.Code)
	}
	if got, err := s.days.Get(ctx, alice.ID, d.ID); err != nil || got.Title != "Secret" {
		t.Fatalf("alice's day changed: %+v %v", got, err)
	}

	// Bob cannot file his own day under Alice's collection or tags.
	aliceCol, _ := s.collections.DefaultID(ctx, alice.ID)
	aliceTag, _ := s.tags.DefaultID(ctx, alice.ID)
	if rr := sessionPost(t, s, "/days", bobToken, dayValues("collection", strconv.FormatInt(aliceCol, 10))); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("bob uses alice's collection: %d", rr.Code)
	}
	if rr := sessionPost(t, s, "/days", bobToken, dayValues("tags", strconv.FormatInt(aliceTag, 10))); rr.Code != http.StatusUnprocessableEntity {
		t.Errorf("bob uses alice's tag: %d", rr.Code)
	}
	body := get(s.Handler(), "/days/new", bobToken).Body.String()
	if strings.Contains(body, `<option value="`+strconv.FormatInt(aliceCol, 10)+`"`) {
		t.Error("bob's form lists alice's collection")
	}
	if strings.Contains(body, `name="tags" value="`+strconv.FormatInt(aliceTag, 10)+`"`) {
		t.Error("bob's tag picker lists alice's tag")
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM days WHERE owner_id = ?`, bob.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("bob created %d days with foreign groups", n)
	}
	for _, p := range []string{"/days/abc", "/days/0", "/days/-1"} {
		if rr := get(s.Handler(), p, bobToken); rr.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d", p, rr.Code)
		}
	}
}

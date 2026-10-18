package web

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"pandadays/internal/days"
)

// TestDayListScreenReaderText checks what assistive tech hears on the home
// list: a page heading to navigate to, and chips announced with their field.
func TestDayListScreenReaderText(t *testing.T) {
	s, _, u, token := authenticatedServer(t)
	ctx := context.Background()
	t1, _ := s.tags.Create(ctx, u.ID, days.GroupInput{Name: "Birthday"}, time.Now())
	t2, _ := s.tags.Create(ctx, u.ID, days.GroupInput{Name: "Mom"}, time.Now())
	form := url.Values{"title": {"Mom's birthday"}, "date": {"1960-03-15"}, "repeat": {"yearly"},
		"tags": {strconv.FormatInt(t1.ID, 10), strconv.FormatInt(t2.ID, 10)}}
	if rr := sessionPost(t, s, "/days", token, form); rr.Code != http.StatusSeeOther {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	for lang, want := range map[string][]string{
		"en": {`<h1 class="visually-hidden">Home</h1>`, `<span class="visually-hidden">Collection:</span>`, `<span class="visually-hidden">Tags:</span>`},
		"zh": {`<h1 class="visually-hidden">首页</h1>`, `<span class="visually-hidden">合集:</span>`, `<span class="visually-hidden">标签:</span>`},
	} {
		body := get(s.Handler(), "/?lang="+lang, token).Body.String()
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s home missing %q", lang, w)
			}
		}
		if n := strings.Count(body, want[2]); n != 1 {
			t.Errorf("%s: tags prefix announced %d times, want once per day", lang, n)
		}
	}
}

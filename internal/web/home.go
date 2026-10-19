package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"pandadays/internal/days"
	"pandadays/internal/i18n"
	"pandadays/internal/share"
)

// dayItem is one row of a day list.
type dayItem struct {
	Day    days.Day
	State  days.Classification
	Label  string
	Target time.Time // effective target in the user's timezone
	// Collection and Tags are only set for the viewer's own days.
	Collection *days.Group
	Tags       []days.Group
	Shared     bool
	ShareOwner string
	Hidden     bool
	HideAction string
	CSRFToken  string
}

type homePage struct {
	pageData
	When  days.When
	Items []dayItem
}

// groupIndex resolves the owner's collections and tags by ID for list chips.
type groupIndex struct {
	collections map[int64]days.Group
	tags        []days.Group
}

func (s *Server) loadGroupIndex(r *http.Request, owner int64) (groupIndex, error) {
	cols, err := s.collections.List(r.Context(), owner)
	if err != nil {
		return groupIndex{}, err
	}
	tags, err := s.tags.List(r.Context(), owner)
	if err != nil {
		return groupIndex{}, err
	}
	idx := groupIndex{collections: make(map[int64]days.Group, len(cols)), tags: tags}
	for _, c := range cols {
		idx.collections[c.ID] = c
	}
	return idx, nil
}

// dayTags returns the day's tags in the owner's tag-list order.
func (g groupIndex) dayTags(d days.Day) []days.Group {
	on := make(map[int64]bool, len(d.TagIDs))
	for _, id := range d.TagIDs {
		on[id] = true
	}
	var out []days.Group
	for _, t := range g.tags {
		if on[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r.Context())
	when := days.ParseWhen(r.URL.Query().Get("when"))
	list, err := s.days.ListActive(r.Context(), u.ID)
	if err != nil {
		s.dayFail(w, r, "list", err)
		return
	}
	groups, err := s.loadGroupIndex(r, u.ID)
	if err != nil {
		s.dayFail(w, r, "list groups for", err)
		return
	}
	shared, err := s.shares.ListForUser(r.Context(), u.ID, false)
	if err != nil {
		s.dayFail(w, r, "list shared days", err)
		return
	}
	sharedDays := make([]days.Day, 0, len(shared))
	sharedByID := make(map[int64]share.SharedDay, len(shared))
	for _, d := range shared {
		sharedDays = append(sharedDays, sharedDayForList(d))
		sharedByID[d.ID] = d
	}
	list = append(list, sharedDays...)
	pageData := newPageData(r)
	if pageData.SearchQuery != "" {
		query := strings.ToLower(pageData.SearchQuery)
		matches := make([]days.Day, 0, len(list))
		for _, day := range list {
			if strings.Contains(strings.ToLower(day.Title), query) {
				matches = append(matches, day)
			}
		}
		list = matches
	}
	now, loc := s.now(), userLocation(u)
	entries := days.HomeList(list, when, now, loc)
	if pageData.SearchQuery != "" {
		entries = append(days.HomeList(list, days.WhenFuture, now, loc),
			days.HomeList(list, days.WhenPast, now, loc)...)
	}
	items := s.dayItems(r, entries, groups)
	for i := range items {
		if sharedDay, ok := sharedByID[items[i].Day.ID]; ok {
			items[i].Shared = true
			items[i].ShareOwner = sharedDay.OwnerUsername
			items[i].HideAction = "/shared/" + strconv.FormatInt(sharedDay.ID, 10) + "/hide"
		}
	}
	page := homePage{pageData: pageData, When: when,
		Items: items}
	w.Header().Add("Vary", "HX-Request")
	if isHTMX(r) && r.Header.Get("HX-History-Restore-Request") == "" {
		s.renderPartial(w, r, "home.html", "home-days", page)
		return
	}
	s.render(w, r, "home.html", page)
}

func sharedDayForList(d share.SharedDay) days.Day {
	return days.Day{
		ID: d.ID, OwnerID: d.OwnerID, Title: d.Title, Date: d.Date, Time: d.Time, Repeat: days.Repeat(d.Repeat),
		StyleColor: d.StyleColor, StyleIcon: d.StyleIcon, StyleBgAttachmentID: d.StyleBgAttachmentID,
		ArchivedAt: d.ArchivedAt,
	}
}

func (s *Server) dayItems(r *http.Request, entries []days.Entry, groups groupIndex) []dayItem {
	lang := i18n.LangFrom(r.Context())
	items := make([]dayItem, 0, len(entries))
	for _, e := range entries {
		item := dayItem{Day: e.Day, State: e.Calc.Classification, Target: e.Calc.Target,
			Label:     s.i18n.T(lang, e.Calc.ListLabel.Key, e.Calc.ListLabel.Args...),
			CSRFToken: CSRFToken(r.Context())}
		for _, tag := range groups.dayTags(e.Day) {
			if !tag.Reserved {
				item.Tags = append(item.Tags, tag)
			}
		}
		if c, ok := groups.collections[e.Day.CollectionID]; ok && !c.Reserved {
			item.Collection = &c
		}
		items = append(items, item)
	}
	return items
}

// allDayItems keeps both halves, upcoming first, using Home's ordering within each.
func (s *Server) allDayItems(r *http.Request, list []days.Day) ([]dayItem, error) {
	u := CurrentUser(r.Context())
	groups, err := s.loadGroupIndex(r, u.ID)
	if err != nil {
		return nil, err
	}
	now, loc := s.now(), userLocation(u)
	entries := append(days.HomeList(list, days.WhenFuture, now, loc), days.HomeList(list, days.WhenPast, now, loc)...)
	return s.dayItems(r, entries, groups), nil
}

func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	list, err := s.days.ListArchived(r.Context(), CurrentUser(r.Context()).ID)
	if err != nil {
		s.dayFail(w, r, "list archived", err)
		return
	}
	items, err := s.allDayItems(r, list)
	if err != nil {
		s.dayFail(w, r, "list groups for archived", err)
		return
	}
	s.render(w, r, "archive.html", struct {
		pageData
		Items []dayItem
	}{newPageData(r), items})
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// breakdownPart is one "N unit" piece of a calendar breakdown.
type breakdownPart struct {
	Value int
	Unit  string
}

func (s *Server) breakdownParts(lang string, b days.Breakdown) []breakdownPart {
	type unitValue struct {
		n    int
		unit string
	}
	values := []unitValue{{b.Years, "year"}, {b.Months, "month"}, {b.Days, "day"}}
	var parts []breakdownPart
	for _, v := range values {
		if v.n == 0 {
			continue
		}
		plural := "other"
		if v.n == 1 {
			plural = "one"
		}
		parts = append(parts, breakdownPart{Value: v.n, Unit: s.i18n.T(lang, "days.unit."+v.unit+"."+plural)})
	}
	return parts
}

// ordinalNumber formats n as an ordinal figure for lang ("3rd" in English, "3" otherwise).
func ordinalNumber(lang string, n int) string {
	s := strconv.Itoa(n)
	if lang != "en" {
		return s
	}
	switch {
	case n%100 >= 11 && n%100 <= 13:
		return s + "th"
	case n%10 == 1:
		return s + "st"
	case n%10 == 2:
		return s + "nd"
	case n%10 == 3:
		return s + "rd"
	}
	return s + "th"
}

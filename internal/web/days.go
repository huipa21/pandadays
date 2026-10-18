package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/days"
	"pandadays/internal/i18n"
)

type dayForm struct {
	Title        string
	Date         string
	Time         string
	Repeat       string
	CollectionID int64
	TagIDs       []int64
	Comment      string
	Color        string
	NoColor      bool
	Icon         string
}

type groupOption struct {
	Group    days.Group
	Selected bool
}

type dayFormPage struct {
	pageData
	Day         days.Day // zero ID for a new day
	Form        dayForm
	Repeats     []days.Repeat
	Collections []groupOption
	Tags        []groupOption
	Error       string
}

type dayPage struct {
	pageData
	Day        days.Day
	State      days.Classification
	Target     time.Time // effective target in the user's timezone
	Big        string    // countdown figure
	Caption    string    // countdown unit and direction, empty for "Today"
	Breakdown  []breakdownPart
	Ordinal    string // repeating days from the first repeat on
	Since      []breakdownPart
	Collection days.Group
	Tags       []days.Group
}

var repeatOptions = []days.Repeat{days.RepeatNone, days.RepeatWeekly, days.RepeatMonthly, days.RepeatYearly}

func (s *Server) registerDayRoutes(mux *http.ServeMux) {
	mux.Handle("GET /days/new", RequireLogin(http.HandlerFunc(s.handleDayNew)))
	mux.Handle("POST /days", RequireLogin(http.HandlerFunc(s.handleDayCreate)))
	mux.Handle("GET /days/{id}", RequireLogin(http.HandlerFunc(s.handleDayView)))
	mux.Handle("GET /days/{id}/edit", RequireLogin(http.HandlerFunc(s.handleDayEditForm)))
	mux.Handle("POST /days/{id}/edit", RequireLogin(http.HandlerFunc(s.handleDayUpdate)))
	mux.Handle("POST /days/{id}/delete", RequireLogin(http.HandlerFunc(s.handleDayDelete)))
	mux.Handle("POST /days/{id}/archive", RequireLogin(http.HandlerFunc(s.handleDayArchive)))
	mux.Handle("POST /days/{id}/unarchive", RequireLogin(http.HandlerFunc(s.handleDayUnarchive)))
	mux.Handle("GET /archive", RequireLogin(http.HandlerFunc(s.handleArchive)))
}

func (s *Server) handleDayArchive(w http.ResponseWriter, r *http.Request) {
	s.setDayArchived(w, r, true)
}

func (s *Server) handleDayUnarchive(w http.ResponseWriter, r *http.Request) {
	s.setDayArchived(w, r, false)
}

func (s *Server) setDayArchived(w http.ResponseWriter, r *http.Request, archived bool) {
	id, ok := s.dayID(w, r)
	if !ok {
		return
	}
	if err := s.days.SetArchived(r.Context(), CurrentUser(r.Context()).ID, id, archived, s.now()); err != nil {
		s.dayFail(w, r, "archive", err)
		return
	}
	authRedirect(w, r, dayPath(id))
}

func dayErrorKey(err error) (string, bool) {
	keys := []struct {
		err error
		key string
	}{
		{days.ErrTitleRequired, "days.error.title_required"},
		{days.ErrTitleTooLong, "days.error.title_too_long"},
		{days.ErrDateInvalid, "days.error.date"},
		{days.ErrTimeInvalid, "days.error.time"},
		{days.ErrRepeatInvalid, "days.error.repeat"},
		{days.ErrCommentTooLong, "days.error.comment_too_long"},
		{days.ErrColorInvalid, "days.error.color"},
		{days.ErrIconInvalid, "days.error.icon"},
		{days.ErrCollectionInvalid, "days.error.collection"},
		{days.ErrTagInvalid, "days.error.tag"},
		{days.ErrQuotaDays, "days.error.quota"},
	}
	for _, k := range keys {
		if errors.Is(err, k.err) {
			return k.key, true
		}
	}
	return "", false
}

// userLocation is the user's timezone, falling back to the server's (TZ).
func userLocation(u *auth.User) *time.Location {
	if u != nil {
		if loc, err := auth.ValidateTimezone(u.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

// parseID returns -1 for malformed values so they fail ownership checks.
func parseID(v string) int64 {
	if v == "" {
		return 0
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return -1
	}
	return id
}

func parseDayForm(r *http.Request) dayForm {
	f := dayForm{
		Title:        r.PostForm.Get("title"),
		Date:         r.PostForm.Get("date"),
		Time:         r.PostForm.Get("time"),
		Repeat:       r.PostForm.Get("repeat"),
		CollectionID: parseID(r.PostForm.Get("collection")),
		Comment:      r.PostForm.Get("comment"),
		Color:        r.PostForm.Get("color"),
		NoColor:      r.PostForm.Get("no_color") != "",
		Icon:         r.PostForm.Get("icon"),
	}
	for _, v := range r.PostForm["tags"] {
		f.TagIDs = append(f.TagIDs, parseID(v))
	}
	return f
}

func (f dayForm) input() days.DayInput {
	in := days.DayInput{Title: f.Title, Date: f.Date, Time: f.Time, Repeat: days.Repeat(f.Repeat),
		CollectionID: f.CollectionID, TagIDs: f.TagIDs, Comment: f.Comment, StyleColor: f.Color, StyleIcon: f.Icon}
	if f.NoColor {
		in.StyleColor = ""
	}
	return in
}

func formFromDay(d days.Day) dayForm {
	f := dayForm{Title: d.Title, Date: d.Date, Time: d.Time, Repeat: string(d.Repeat), CollectionID: d.CollectionID,
		TagIDs: d.TagIDs, Comment: d.Comment, Color: d.StyleColor, NoColor: d.StyleColor == "", Icon: d.StyleIcon}
	if f.NoColor {
		f.Color = defaultGroupColor
	}
	return f
}

func (s *Server) dayID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id := parseID(r.PathValue("id"))
	if id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

func (s *Server) dayFail(w http.ResponseWriter, r *http.Request, op string, err error) {
	if errors.Is(err, days.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	s.log.Error(op+" day", "err", err)
	s.authError(w, r, "error.internal", http.StatusInternalServerError)
}

func (s *Server) renderDayForm(w http.ResponseWriter, r *http.Request, status int, d days.Day, form dayForm, errKey string) {
	owner := CurrentUser(r.Context()).ID
	cols, err := s.collections.List(r.Context(), owner)
	if err != nil {
		s.dayFail(w, r, "list collections for", err)
		return
	}
	tags, err := s.tags.List(r.Context(), owner)
	if err != nil {
		s.dayFail(w, r, "list tags for", err)
		return
	}
	selectedTags := make(map[int64]bool, len(form.TagIDs))
	for _, id := range form.TagIDs {
		selectedTags[id] = true
	}
	page := dayFormPage{pageData: newPageData(r), Day: d, Form: form, Repeats: repeatOptions, Error: errKey}
	for _, g := range cols {
		page.Collections = append(page.Collections, groupOption{Group: g, Selected: g.ID == form.CollectionID})
	}
	for _, g := range tags {
		page.Tags = append(page.Tags, groupOption{Group: g, Selected: selectedTags[g.ID]})
	}
	s.renderStatus(w, r, "day_form.html", status, page)
}

func (s *Server) handleDayNew(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r.Context())
	col, err := s.collections.DefaultID(r.Context(), u.ID)
	if err != nil {
		s.dayFail(w, r, "default collection for", err)
		return
	}
	tag, err := s.tags.DefaultID(r.Context(), u.ID)
	if err != nil {
		s.dayFail(w, r, "default tag for", err)
		return
	}
	form := dayForm{
		Date:         s.now().In(userLocation(u)).Format(time.DateOnly),
		Repeat:       string(days.RepeatNone),
		CollectionID: col,
		TagIDs:       []int64{tag},
		Color:        defaultGroupColor,
		NoColor:      true,
	}
	s.renderDayForm(w, r, http.StatusOK, days.Day{}, form, "")
}

func (s *Server) handleDayCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	form := parseDayForm(r)
	d, err := s.days.Create(r.Context(), CurrentUser(r.Context()).ID, form.input(), time.Now())
	if key, ok := dayErrorKey(err); ok {
		if form.NoColor || form.Color == "" {
			form.Color, form.NoColor = defaultGroupColor, true
		}
		s.renderDayForm(w, r, http.StatusUnprocessableEntity, days.Day{}, form, key)
		return
	}
	if err != nil {
		s.dayFail(w, r, "create", err)
		return
	}
	http.Redirect(w, r, dayPath(d.ID), http.StatusSeeOther)
}

func dayPath(id int64) string { return "/days/" + strconv.FormatInt(id, 10) }

func (s *Server) handleDayView(w http.ResponseWriter, r *http.Request) {
	id, ok := s.dayID(w, r)
	if !ok {
		return
	}
	u := CurrentUser(r.Context())
	d, err := s.days.Get(r.Context(), u.ID, id)
	if err != nil {
		s.dayFail(w, r, "get", err)
		return
	}
	page := dayPage{pageData: newPageData(r), Day: d}
	calc, err := days.Calculate(d.Schedule(), s.now(), userLocation(u))
	if err != nil {
		s.dayFail(w, r, "calculate", err)
		return
	}
	lang := i18n.LangFrom(r.Context())
	timed := d.Time != ""
	page.State, page.Target = calc.Classification, calc.Target
	page.Big, page.Caption = s.countdown(lang, calc.ListLabel)
	page.Breakdown = s.breakdownParts(lang, calc.Breakdown, timed)
	if d.Repeat != days.RepeatNone && calc.OccurrenceOrdinal > 0 {
		page.Ordinal = s.i18n.T(lang, "days.card.ordinal."+string(d.Repeat), ordinalNumber(lang, calc.OccurrenceOrdinal))
	}
	if calc.SinceOriginal != nil {
		page.Since = s.breakdownParts(lang, *calc.SinceOriginal, timed)
	}
	if page.Collection, err = s.collections.Get(r.Context(), u.ID, d.CollectionID); err != nil {
		s.dayFail(w, r, "get collection for", err)
		return
	}
	groups, err := s.loadGroupIndex(r, u.ID)
	if err != nil {
		s.dayFail(w, r, "list tags for", err)
		return
	}
	page.Tags = groups.dayTags(d)
	s.render(w, r, "day.html", page)
}

func (s *Server) handleDayEditForm(w http.ResponseWriter, r *http.Request) {
	id, ok := s.dayID(w, r)
	if !ok {
		return
	}
	d, err := s.days.Get(r.Context(), CurrentUser(r.Context()).ID, id)
	if err != nil {
		s.dayFail(w, r, "get", err)
		return
	}
	s.renderDayForm(w, r, http.StatusOK, d, formFromDay(d), "")
}

func (s *Server) handleDayUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := s.dayID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	owner := CurrentUser(r.Context()).ID
	form := parseDayForm(r)
	_, err := s.days.Update(r.Context(), owner, id, form.input(), time.Now())
	if key, ok := dayErrorKey(err); ok {
		d, gerr := s.days.Get(r.Context(), owner, id)
		if gerr != nil {
			s.dayFail(w, r, "get", gerr)
			return
		}
		if form.NoColor || form.Color == "" {
			form.Color, form.NoColor = defaultGroupColor, true
		}
		s.renderDayForm(w, r, http.StatusUnprocessableEntity, d, form, key)
		return
	}
	if err != nil {
		s.dayFail(w, r, "update", err)
		return
	}
	http.Redirect(w, r, dayPath(id), http.StatusSeeOther)
}

func (s *Server) handleDayDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.dayID(w, r)
	if !ok {
		return
	}
	owner := CurrentUser(r.Context()).ID
	if err := s.days.Delete(r.Context(), owner, id); err != nil {
		s.dayFail(w, r, "delete", err)
		return
	}
	s.log.Info("deleted day", "user_id", owner, "id", id)
	authRedirect(w, r, "/")
}

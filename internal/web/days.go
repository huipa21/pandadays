package web

import (
	"errors"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pandadays/internal/auth"
	"pandadays/internal/days"
	"pandadays/internal/i18n"
	"pandadays/internal/media"
	"pandadays/internal/share"
)

type dayForm struct {
	Private      bool
	Title        string
	Date         string
	Time         string
	Repeat       string
	Calendar     string
	LunarYear    string
	LunarMonth   string
	LunarDay     string
	CollectionID int64
	TagIDs       []int64
	TagNames     []string
	TagQuery     string
	Comment      string
	Color        string
	NoColor      bool
	Icon         string
	LibraryIDs   []int64 // existing attachments to copy onto a new day
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
	Library     []media.LibraryItem
	Picked      map[int64]bool
	Error       string
	LunarMonths []lunarMonthOption
	LunarData   string
}

type dayPage struct {
	pageData
	Day         days.Day
	State       days.Classification
	Target      time.Time // effective target in the user's timezone
	Direction   string
	Clock       string
	Breakdown   []breakdownPart
	DaysOnly    bool
	Created     time.Time // zero for shared days
	Ordinal     string    // repeating days from the first repeat on
	Since       []breakdownPart
	SinceClock  string
	Collection  days.Group
	Tags        []days.Group
	Attachments []media.Attachment
	Usage       media.Usage
	Shared      bool
	ShareOwner  string
	ShowComment bool
	ShareRows   []shareRow
	Library     []media.LibraryItem
	Picked      map[int64]bool
}

type attachmentRow struct {
	media.Attachment
	CSRFToken string
	Shared    bool
	PlayerID  string
}

func (p dayPage) AttachmentRow(a media.Attachment, scope string) attachmentRow {
	return attachmentRow{
		Attachment: a,
		CSRFToken:  p.CSRFToken,
		Shared:     p.Shared,
		PlayerID:   scope + "-player-" + strconv.FormatInt(a.ID, 10),
	}
}

var repeatOptions = []days.Repeat{days.RepeatNone, days.RepeatWeekly, days.RepeatMonthly, days.RepeatYearly}

func (s *Server) registerDayRoutes(mux *http.ServeMux) {
	mux.Handle("GET /days/new", RequireLogin(http.HandlerFunc(s.handleDayNew)))
	mux.Handle("GET /days/calendar", RequireLogin(http.HandlerFunc(s.handleDayCalendar)))
	mux.Handle("POST /days", RequireLogin(http.HandlerFunc(s.handleDayCreate)))
	mux.Handle("GET /days/{id}", RequireLogin(http.HandlerFunc(s.handleDayView)))
	mux.Handle("GET /days/{id}/edit", RequireLogin(http.HandlerFunc(s.handleDayEditForm)))
	mux.Handle("POST /days/{id}/edit", RequireLogin(http.HandlerFunc(s.handleDayUpdate)))
	mux.Handle("POST /days/{id}/delete", RequireLogin(http.HandlerFunc(s.handleDayDelete)))
	mux.Handle("POST /days/{id}/archive", RequireLogin(http.HandlerFunc(s.handleDayArchive)))
	mux.Handle("POST /days/{id}/unarchive", RequireLogin(http.HandlerFunc(s.handleDayUnarchive)))
	mux.Handle("POST /days/{id}/play", RequireLogin(http.HandlerFunc(s.handleDayPlay)))
	mux.Handle("POST /days/{id}/pause", RequireLogin(http.HandlerFunc(s.handleDayPause)))
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

func (s *Server) handleDayPlay(w http.ResponseWriter, r *http.Request) {
	s.setDayArchiveLive(w, r, true)
}

func (s *Server) handleDayPause(w http.ResponseWriter, r *http.Request) {
	s.setDayArchiveLive(w, r, false)
}

func (s *Server) setDayArchiveLive(w http.ResponseWriter, r *http.Request, live bool) {
	id, ok := s.dayID(w, r)
	if !ok {
		return
	}
	if err := s.days.SetArchiveLive(r.Context(), CurrentUser(r.Context()).ID, id, live); err != nil {
		s.dayFail(w, r, "set archive play state for", err)
		return
	}
	target := dayPath(id)
	if r.PostFormValue("display") == "days" {
		target += "?display=days"
	}
	authRedirect(w, r, target)
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
		{days.ErrCalendarInvalid, "days.error.calendar"},
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
	return groupErrorKey(err)
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
		Private:      r.PostForm.Get("private") != "",
		Title:        r.PostForm.Get("title"),
		Date:         r.PostForm.Get("date"),
		Time:         r.PostForm.Get("time"),
		Repeat:       r.PostForm.Get("repeat"),
		Calendar:     r.PostForm.Get("calendar"),
		LunarYear:    r.PostForm.Get("lunar_year"),
		LunarMonth:   r.PostForm.Get("lunar_month"),
		LunarDay:     r.PostForm.Get("lunar_day"),
		CollectionID: parseID(r.PostForm.Get("collection")),
		Comment:      r.PostForm.Get("comment"),
		Color:        r.PostForm.Get("color"),
		NoColor:      r.PostForm.Get("no_color") != "",
		Icon:         r.PostForm.Get("icon"),
		TagNames:     r.PostForm["new_tags"],
		TagQuery:     r.PostForm.Get("tag_query"),
	}
	for _, v := range r.PostForm["tags"] {
		f.TagIDs = append(f.TagIDs, parseID(v))
	}
	seen := make(map[int64]bool)
	for _, v := range r.PostForm["library"] {
		if id := parseID(v); id != 0 && !seen[id] {
			seen[id] = true
			f.LibraryIDs = append(f.LibraryIDs, id)
		}
	}
	return f
}

func (f dayForm) input() days.DayInput {
	in := days.DayInput{Private: f.Private, Title: f.Title, Date: f.Date, Time: f.Time, Repeat: days.Repeat(f.Repeat),
		Calendar:     days.Calendar(f.Calendar),
		CollectionID: f.CollectionID, TagIDs: f.TagIDs, TagNames: append([]string(nil), f.TagNames...), Comment: f.Comment, StyleColor: f.Color, StyleIcon: f.Icon}
	if in.Calendar == days.CalendarLunar {
		year, _ := strconv.Atoi(f.LunarYear)
		month, _ := strconv.Atoi(f.LunarMonth)
		day, _ := strconv.Atoi(f.LunarDay)
		in.Date, _ = days.LunarToSolar(days.LunarDate{Year: year, Month: month, Day: day})
	}
	for _, name := range strings.Split(f.TagQuery, "#")[1:] {
		if name = strings.TrimSpace(name); name != "" {
			in.TagNames = append(in.TagNames, name)
		}
	}
	if f.NoColor {
		in.StyleColor = ""
	}
	return in
}

func formFromDay(d days.Day) dayForm {
	f := dayForm{Private: d.Private, Title: d.Title, Date: d.Date, Time: d.Time, Repeat: string(d.Repeat), CollectionID: d.CollectionID,
		Calendar: string(d.Calendar),
		TagIDs:   d.TagIDs, Comment: d.Comment, Color: d.StyleColor, NoColor: d.StyleColor == "", Icon: d.StyleIcon}
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
	page.LunarData, err = lunarPickerData()
	if err != nil {
		s.dayFail(w, r, "load lunar calendar for", err)
		return
	}
	if page.Form.Calendar == "" {
		page.Form.Calendar = string(days.CalendarGregorian)
	}
	if page.Form.LunarYear == "" && page.Form.LunarMonth == "" && page.Form.LunarDay == "" {
		if lunar, err := days.SolarToLunar(form.Date); err == nil {
			page.Form.LunarYear = strconv.Itoa(lunar.Year)
			page.Form.LunarMonth = strconv.Itoa(lunar.Month)
			page.Form.LunarDay = strconv.Itoa(lunar.Day)
		}
	}
	for month := 1; month <= 12; month++ {
		page.LunarMonths = append(page.LunarMonths,
			lunarMonthOption{Value: strconv.Itoa(month), Key: "days.lunar.month." + strconv.Itoa(month)},
			lunarMonthOption{Value: strconv.Itoa(-month), Key: "days.lunar.month." + strconv.Itoa(month), Leap: true})
	}
	for _, g := range cols {
		page.Collections = append(page.Collections, groupOption{Group: g, Selected: g.ID == form.CollectionID})
	}
	for _, g := range tags {
		if !g.Reserved {
			page.Tags = append(page.Tags, groupOption{Group: g, Selected: selectedTags[g.ID]})
		}
	}
	if d.ID == 0 {
		if page.Library, err = s.media.ListOwner(r.Context(), owner); err != nil {
			s.dayFail(w, r, "list media library for", err)
			return
		}
		page.Picked = make(map[int64]bool, len(form.LibraryIDs))
		for _, id := range form.LibraryIDs {
			page.Picked[id] = true
		}
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
	form := dayForm{
		Private:      days.IsPrivate(r.Context()),
		Date:         s.now().In(userLocation(u)).Format(time.DateOnly),
		Time:         "00:00",
		Repeat:       string(days.RepeatNone),
		CollectionID: col,
		Color:        defaultGroupColor,
		NoColor:      true,
	}
	s.renderDayForm(w, r, http.StatusOK, days.Day{}, form, "")
}

func (s *Server) handleDayCreate(w http.ResponseWriter, r *http.Request) {
	if isDayCreateUpload(r) {
		if r.MultipartForm == nil {
			if !s.limitDayCreateForm(w, r) {
				return
			}
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				if r.MultipartForm != nil {
					r.MultipartForm.RemoveAll()
				}
				s.uploadReadError(w, r, err)
				return
			}
		}
		defer r.MultipartForm.RemoveAll()
	}
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	form := parseDayForm(r)
	owner := CurrentUser(r.Context()).ID
	library := make([]media.Attachment, 0, len(form.LibraryIDs))
	for _, id := range form.LibraryIDs {
		a, err := s.media.Get(r.Context(), owner, id)
		if errors.Is(err, media.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.attachmentError(w, r, "get library item for new day", err)
			return
		}
		file, err := s.media.Open(a)
		if err != nil {
			s.attachmentError(w, r, "check library file for new day", err)
			return
		}
		if err := file.Close(); err != nil {
			s.attachmentError(w, r, "close library file for new day", err)
			return
		}
		library = append(library, a)
	}
	var uploads []*multipart.FileHeader
	if r.MultipartForm != nil {
		uploads = r.MultipartForm.File["file"]
	}
	var prepared []*media.PreparedUpload
	var prepareErr error
	if len(uploads) > 0 || len(library) > 0 {
		remaining, err := s.media.NewDayRemaining(r.Context(), owner)
		if err != nil {
			s.attachmentError(w, r, "check new day attachments", err)
			return
		}
		prepared, err = s.prepareUploads(uploads)
		if err != nil {
			if errors.Is(err, media.ErrDuplicate) || errors.Is(err, media.ErrQuota) ||
				errors.Is(err, media.ErrTooLarge) || errors.Is(err, media.ErrImageTooLarge) ||
				errors.Is(err, media.ErrUploadStream) {
				s.attachmentSaveError(w, r, "prepare new day attachments", err)
				return
			}
			prepareErr = err
		}
		defer func() {
			if err := closePreparedUploads(prepared); err != nil {
				s.log.Error("clean up prepared new day attachments", "err", err)
			}
		}()
		sizes := make([]int64, 0, len(uploads)+len(library))
		if prepareErr == nil {
			for _, file := range prepared {
				sizes = append(sizes, file.Size())
			}
		} else {
			for _, file := range uploads {
				sizes = append(sizes, file.Size)
			}
		}
		for _, a := range library {
			sizes = append(sizes, a.Size)
		}
		for _, size := range sizes {
			if remaining <= 0 || size > remaining {
				s.renderDayForm(w, r, http.StatusRequestEntityTooLarge, days.Day{}, form, "attachments.error.limit")
				return
			}
			remaining -= size
		}
		duplicateErr := error(nil)
		if prepareErr == nil {
			duplicateErr = s.checkUploadDuplicates(r.Context(), owner, prepared)
		} else {
			duplicateErr = s.checkRawUploadDuplicates(r.Context(), owner, uploads)
		}
		if err := duplicateErr; errors.Is(err, media.ErrDuplicate) {
			s.renderDayForm(w, r, http.StatusConflict, days.Day{}, form, "attachments.error.duplicate")
			return
		} else if err != nil {
			s.attachmentError(w, r, "check new day attachments", err)
			return
		}
	}
	d, err := s.days.Create(r.Context(), owner, form.input(), s.now())
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
	r = r.WithContext(days.WithPrivate(r.Context(), d.Private))
	for _, a := range library {
		if _, err := s.media.CopyToDay(r.Context(), owner, a.ID, d.ID); err != nil {
			s.log.Error("copy library attachment for new day", "day_id", d.ID, "attachment_id", a.ID, "err", err)
			s.renderDayForm(w, r, http.StatusInternalServerError, d, formFromDay(d), "attachments.create_error")
			return
		}
	}
	if prepareErr != nil {
		s.log.Error("prepare attachment for new day", "day_id", d.ID, "err", prepareErr)
		s.renderDayForm(w, r, http.StatusInternalServerError, d, formFromDay(d), "attachments.create_error")
		return
	}
	for i, header := range uploads {
		_, err := s.media.UploadPrepared(r.Context(), owner, d.ID, header.Filename, prepared[i])
		if err != nil {
			s.log.Error("upload attachment for new day", "day_id", d.ID, "err", err)
			// Keep the saved day and successful uploads; retries must not create duplicates.
			s.renderDayForm(w, r, http.StatusInternalServerError, d, formFromDay(d), "attachments.create_error")
			return
		}
	}
	if d.Private {
		authRedirect(w, r.WithContext(days.WithPrivate(r.Context(), false)), "/")
		return
	}
	authRedirect(w, r, dayPath(d.ID))
}

func dayPath(id int64) string { return "/days/" + strconv.FormatInt(id, 10) }

func (s *Server) handleDayView(w http.ResponseWriter, r *http.Request) {
	id, ok := s.dayID(w, r)
	if !ok {
		return
	}
	u := CurrentUser(r.Context())
	d, err := s.days.Get(r.Context(), u.ID, id)
	shared := false
	shareOwner := ""
	showComment := true
	if errors.Is(err, days.ErrNotFound) && !days.IsPrivate(r.Context()) {
		var sharedDay share.SharedDay
		sharedDay, err = s.shares.GetShared(r.Context(), u.ID, id)
		if err == nil {
			d = days.Day{
				ID: sharedDay.ID, OwnerID: sharedDay.OwnerID, Title: sharedDay.Title, Date: sharedDay.Date,
				Time: sharedDay.Time, Repeat: days.Repeat(sharedDay.Repeat), Calendar: days.Calendar(sharedDay.Calendar), Comment: sharedDay.Comment,
				StyleColor: sharedDay.StyleColor, StyleIcon: sharedDay.StyleIcon,
				StyleBgAttachmentID: sharedDay.StyleBgAttachmentID, ArchivedAt: sharedDay.ArchivedAt, ArchiveLive: sharedDay.ArchiveLive,
			}
			shared, shareOwner, showComment = true, sharedDay.OwnerUsername, sharedDay.ShowComment
		} else if errors.Is(err, share.ErrNotFound) {
			err = days.ErrNotFound
		}
	}
	if err != nil {
		s.dayFail(w, r, "get", err)
		return
	}
	page := dayPage{pageData: newPageData(r), Day: d, Shared: shared, ShareOwner: shareOwner, ShowComment: showComment,
		DaysOnly: r.URL.Query().Get("display") == "days"}
	loc := userLocation(u)
	if !d.CreatedAt.IsZero() {
		page.Created = d.CreatedAt.In(loc)
	}
	calc, err := days.Calculate(d.Schedule(), d.CountAt(s.now()), loc)
	if err != nil {
		s.dayFail(w, r, "calculate", err)
		return
	}
	lang := i18n.LangFrom(r.Context())
	timed := d.Time != ""
	page.State, page.Target = calc.Classification, calc.Target
	page.Direction = s.i18n.T(lang, "days.today")
	if calc.ListLabel.Key != "days.today" {
		direction := "left"
		if calc.Classification == days.Past {
			direction = "ago"
		}
		page.Direction = s.i18n.T(lang, "days.card.time."+direction)
	}
	breakdown, since := calc.Breakdown, calc.SinceOriginal
	if page.DaysOnly {
		breakdown, since = calc.DaysOnly, calc.SinceOriginalDays
	}
	parts := func(b days.Breakdown) []breakdownPart {
		result := s.breakdownParts(lang, b)
		if page.DaysOnly && len(result) == 0 {
			result = []breakdownPart{{Value: 0, Unit: s.i18n.T(lang, "days.unit.day.other")}}
		}
		return result
	}
	page.Breakdown = parts(breakdown)
	if timed {
		page.Clock = s.i18n.T(lang, "days.card.hm", breakdown.Hours, breakdown.Minutes)
	}
	if d.Repeat != days.RepeatNone && calc.OccurrenceOrdinal > 0 {
		page.Ordinal = s.i18n.T(lang, "days.card.ordinal."+string(d.Repeat), ordinalNumber(lang, calc.OccurrenceOrdinal))
	}
	if since != nil {
		page.Since = parts(*since)
		if timed {
			page.SinceClock = s.i18n.T(lang, "days.card.hm", since.Hours, since.Minutes)
		}
	}
	if !shared {
		page.Library, err = s.media.ListOwner(r.Context(), u.ID)
		if err != nil {
			s.dayFail(w, r, "list media library for", err)
			return
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
		shares, listErr := s.shares.List(r.Context(), u.ID, share.TargetDay, d.ID)
		err = listErr
		if err != nil {
			s.dayFail(w, r, "list shares for", err)
			return
		}
		page.ShareRows = shareRows(shares)
	}
	if shared {
		page.Attachments, err = s.media.ListAccessibleDay(r.Context(), u.ID, d.ID)
	} else {
		page.Attachments, err = s.media.ListDay(r.Context(), u.ID, d.ID)
	}
	if err != nil {
		s.dayFail(w, r, "list attachments for", err)
		return
	}
	if !shared {
		page.Usage, err = s.media.Usage(r.Context(), u.ID, d.ID)
		if err != nil {
			s.dayFail(w, r, "read attachment usage for", err)
			return
		}
	}
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
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	owner := CurrentUser(r.Context()).ID
	form := parseDayForm(r)
	d, err := s.days.Update(r.Context(), owner, id, form.input(), s.now())
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
	r = r.WithContext(days.WithPrivate(r.Context(), d.Private))
	authRedirect(w, r, dayPath(id))
}

func (s *Server) handleDayDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := s.dayID(w, r)
	if !ok {
		return
	}
	owner := CurrentUser(r.Context()).ID
	files, err := s.days.DeleteWithAttachments(r.Context(), owner, id)
	if err != nil {
		s.dayFail(w, r, "delete", err)
		return
	}
	if err := s.media.RemoveFiles(owner, files); err != nil {
		s.log.Error("remove attachment files for deleted day", "user_id", owner, "day_id", id, "err", err)
	}
	if err := s.media.Collect(r.Context()); err != nil {
		s.log.Error("remove unreferenced media for deleted day", "user_id", owner, "day_id", id, "err", err)
	}
	s.log.Info("deleted day", "user_id", owner, "id", id)
	authRedirect(w, r, "/")
}

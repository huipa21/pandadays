package web

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"pandadays/internal/days"
)

// groupSection describes how one kind of group (collections or tags) is
// routed and labelled; both kinds share handlers and templates.
type groupSection struct {
	Key  string // i18n key prefix and path segment, e.g. "collections"
	Base string // URL prefix, e.g. "/collections"
}

type groupForm struct {
	Name    string
	Color   string
	NoColor bool
}

type groupsPage struct {
	pageData
	Section groupSection
	Groups  []days.Group
	Form    groupForm
	Error   string
}

type groupPage struct {
	pageData
	Section      groupSection
	Group        days.Group
	Form         groupForm
	Error        string
	HideArchived bool
	Items        []dayItem
}

const defaultGroupColor = "#2f855a"

type groupHandlers struct {
	s       *Server
	store   *days.Groups
	section groupSection
}

func (s *Server) registerGroupRoutes(mux *http.ServeMux, store *days.Groups, key string) {
	h := &groupHandlers{s: s, store: store, section: groupSection{Key: key, Base: "/" + key}}
	base := h.section.Base
	mux.Handle("GET "+base, RequireLogin(http.HandlerFunc(h.list)))
	mux.Handle("POST "+base, RequireLogin(http.HandlerFunc(h.create)))
	mux.Handle("GET "+base+"/{id}", RequireLogin(http.HandlerFunc(h.detail)))
	mux.Handle("POST "+base+"/{id}/edit", RequireLogin(http.HandlerFunc(h.update)))
	mux.Handle("POST "+base+"/{id}/delete", RequireLogin(http.HandlerFunc(h.delete)))
}

func groupErrorKey(err error) (string, bool) {
	switch {
	case errors.Is(err, days.ErrNameRequired):
		return "groups.error.name_required", true
	case errors.Is(err, days.ErrNameTooLong):
		return "groups.error.name_too_long", true
	case errors.Is(err, days.ErrNameReserved):
		return "groups.error.name_reserved", true
	case errors.Is(err, days.ErrNameTaken):
		return "groups.error.name_taken", true
	case errors.Is(err, days.ErrColorInvalid):
		return "groups.error.color", true
	case errors.Is(err, days.ErrReserved):
		return "groups.error.reserved", true
	}
	return "", false
}

func parseGroupForm(r *http.Request) groupForm {
	f := groupForm{Name: r.PostForm.Get("name"), Color: r.PostForm.Get("color"), NoColor: r.PostForm.Get("no_color") != ""}
	if f.NoColor {
		f.Color = ""
	}
	return f
}

func (h *groupHandlers) groupID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

func (h *groupHandlers) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	if errors.Is(err, days.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	h.s.log.Error(op+" "+h.section.Key, "err", err)
	h.s.authError(w, r, "error.internal", http.StatusInternalServerError)
}

func (h *groupHandlers) renderList(w http.ResponseWriter, r *http.Request, status int, form groupForm, errKey string) {
	groups, err := h.store.List(r.Context(), CurrentUser(r.Context()).ID)
	if err != nil {
		h.fail(w, r, "list", err)
		return
	}
	if form.Color == "" && !form.NoColor {
		form.Color = defaultGroupColor
	}
	h.s.renderStatus(w, r, "groups.html", status, groupsPage{pageData: newPageData(r), Section: h.section,
		Groups: groups, Form: form, Error: errKey})
}

func (h *groupHandlers) list(w http.ResponseWriter, r *http.Request) {
	h.renderList(w, r, http.StatusOK, groupForm{}, "")
}

func (h *groupHandlers) create(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	form := parseGroupForm(r)
	_, err := h.store.Create(r.Context(), CurrentUser(r.Context()).ID,
		days.GroupInput{Name: form.Name, Color: form.Color}, time.Now())
	if key, ok := groupErrorKey(err); ok {
		h.renderList(w, r, http.StatusUnprocessableEntity, form, key)
		return
	}
	if err != nil {
		h.fail(w, r, "create", err)
		return
	}
	http.Redirect(w, r, h.section.Base, http.StatusSeeOther)
}

func (h *groupHandlers) renderDetail(w http.ResponseWriter, r *http.Request, status int, g days.Group, form groupForm, errKey string) {
	hideArchived := r.URL.Query().Get("hide_archived") == "1"
	owner := CurrentUser(r.Context()).ID
	var list []days.Day
	var err error
	if h.section.Key == "collections" {
		list, err = h.s.days.ListCollection(r.Context(), owner, g.ID, hideArchived)
	} else {
		list, err = h.s.days.ListTag(r.Context(), owner, g.ID, hideArchived)
	}
	if err != nil {
		h.fail(w, r, "list days in", err)
		return
	}
	items, err := h.s.allDayItems(r, list)
	if err != nil {
		h.fail(w, r, "list day groups in", err)
		return
	}
	page := groupPage{pageData: newPageData(r), Section: h.section,
		Group: g, Form: form, Error: errKey, HideArchived: hideArchived, Items: items}
	w.Header().Add("Vary", "HX-Request")
	if r.Method == http.MethodGet && isHTMX(r) && r.Header.Get("HX-History-Restore-Request") == "" {
		h.s.renderPartial(w, r, "group.html", "group-days", page)
		return
	}
	h.s.renderStatus(w, r, "group.html", status, page)
}

func formFromGroup(g days.Group) groupForm {
	f := groupForm{Name: g.Name, Color: g.Color, NoColor: g.Color == ""}
	if f.NoColor {
		f.Color = defaultGroupColor
	}
	return f
}

func (h *groupHandlers) detail(w http.ResponseWriter, r *http.Request) {
	id, ok := h.groupID(w, r)
	if !ok {
		return
	}
	g, err := h.store.Get(r.Context(), CurrentUser(r.Context()).ID, id)
	if err != nil {
		h.fail(w, r, "get", err)
		return
	}
	h.renderDetail(w, r, http.StatusOK, g, formFromGroup(g), "")
}

func (h *groupHandlers) update(w http.ResponseWriter, r *http.Request) {
	id, ok := h.groupID(w, r)
	if !ok {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	owner := CurrentUser(r.Context()).ID
	form := parseGroupForm(r)
	_, err := h.store.Update(r.Context(), owner, id, days.GroupInput{Name: form.Name, Color: form.Color})
	if key, ok := groupErrorKey(err); ok {
		g, gerr := h.store.Get(r.Context(), owner, id)
		if gerr != nil {
			h.fail(w, r, "get", gerr)
			return
		}
		if form.NoColor {
			form.Color = defaultGroupColor
		}
		h.renderDetail(w, r, http.StatusUnprocessableEntity, g, form, key)
		return
	}
	if err != nil {
		h.fail(w, r, "update", err)
		return
	}
	http.Redirect(w, r, h.section.Base+"/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

func (h *groupHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := h.groupID(w, r)
	if !ok {
		return
	}
	owner := CurrentUser(r.Context()).ID
	err := h.store.Delete(r.Context(), owner, id)
	if errors.Is(err, days.ErrReserved) {
		g, gerr := h.store.Get(r.Context(), owner, id)
		if gerr != nil {
			h.fail(w, r, "get", gerr)
			return
		}
		h.renderDetail(w, r, http.StatusUnprocessableEntity, g, formFromGroup(g), "groups.error.reserved")
		return
	}
	if err != nil {
		h.fail(w, r, "delete", err)
		return
	}
	h.s.log.Info("deleted "+h.section.Key, "user_id", owner, "id", id)
	authRedirect(w, r, h.section.Base)
}

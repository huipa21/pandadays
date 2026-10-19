package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"pandadays/internal/days"
	"pandadays/internal/share"
)

func (s *Server) registerShareRoutes(mux *http.ServeMux) {
	mux.Handle("GET /messages", RequireLogin(http.HandlerFunc(s.handleInbox)))
	mux.Handle("POST /messages/{id}/accept", RequireLogin(http.HandlerFunc(s.acceptInvitation)))
	mux.Handle("POST /messages/{id}/reject", RequireLogin(http.HandlerFunc(s.rejectInvitation)))
	mux.Handle("GET /shared", RequireLogin(http.HandlerFunc(s.handleSharedList)))
	mux.Handle("POST /days/{id}/shares", RequireLogin(http.HandlerFunc(s.createDayShare)))
	mux.Handle("POST /collections/{id}/shares", RequireLogin(http.HandlerFunc(s.createCollectionShare)))
	mux.Handle("POST /shares/{id}/edit", RequireLogin(http.HandlerFunc(s.updateShare)))
	mux.Handle("POST /shares/{id}/revoke", RequireLogin(http.HandlerFunc(s.revokeShare)))
	mux.Handle("POST /shared/{id}/hide", RequireLogin(http.HandlerFunc(s.hideSharedDay)))
	mux.Handle("POST /shared/{id}/unhide", RequireLogin(http.HandlerFunc(s.unhideSharedDay)))
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	before := int64(0)
	if raw := r.URL.Query().Get("before"); raw != "" {
		var err error
		before, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || before <= 0 {
			s.authError(w, r, "error.bad_request", http.StatusBadRequest)
			return
		}
	}
	items, more, err := s.shares.Inbox(r.Context(), CurrentUser(r.Context()).ID, before)
	if err != nil {
		s.shareError(w, r, "list invitations", err)
		return
	}
	next := int64(0)
	if more {
		next = items[len(items)-1].ShareID
	}
	page := struct {
		pageData
		Invitations []share.Invitation
		Next        int64
	}{newPageData(r), items, next}
	w.Header().Add("Vary", "HX-Request")
	if isHTMX(r) && r.Header.Get("HX-History-Restore-Request") == "" {
		s.renderPartial(w, r, "messages.html", "message-page", page)
		return
	}
	s.render(w, r, "messages.html", page)
}

func (s *Server) acceptInvitation(w http.ResponseWriter, r *http.Request) {
	s.respondInvitation(w, r, true)
}

func (s *Server) rejectInvitation(w http.ResponseWriter, r *http.Request) {
	s.respondInvitation(w, r, false)
}

func (s *Server) respondInvitation(w http.ResponseWriter, r *http.Request, accept bool) {
	id, ok := s.sharePathID(w, r)
	if !ok {
		return
	}
	if err := s.shares.Respond(r.Context(), CurrentUser(r.Context()).ID, id, accept); err != nil {
		s.shareError(w, r, "respond to invitation", err)
		return
	}
	authRedirect(w, r, "/messages")
}

func (s *Server) createDayShare(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sharePathID(w, r)
	if !ok {
		return
	}
	s.createShare(w, r, share.TargetDay, id)
}

func (s *Server) createCollectionShare(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sharePathID(w, r)
	if !ok {
		return
	}
	s.createShare(w, r, share.TargetCollection, id)
}

func (s *Server) createShare(w http.ResponseWriter, r *http.Request, targetType share.TargetType, targetID int64) {
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	_, err := s.shares.Create(r.Context(), CurrentUser(r.Context()).ID, targetType, targetID,
		parseShareRecipients(r.Form.Get("recipients")), r.Form.Get("show_comment") != "", s.now())
	if err != nil {
		s.shareError(w, r, "create", err)
		return
	}
	authRedirect(w, r, shareTargetPath(targetType, targetID))
}

func parseShareRecipients(raw string) []string {
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
}

func (s *Server) updateShare(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sharePathID(w, r)
	if !ok {
		return
	}
	owner := CurrentUser(r.Context()).ID
	targetType, targetID, err := s.shares.Target(r.Context(), owner, id)
	if err != nil {
		s.shareError(w, r, "get", err)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	if err := s.shares.Update(r.Context(), owner, id, parseShareRecipients(r.Form.Get("recipients")),
		r.Form.Get("show_comment") != ""); err != nil {
		s.shareError(w, r, "update", err)
		return
	}
	authRedirect(w, r, shareTargetPath(targetType, targetID))
}

func (s *Server) revokeShare(w http.ResponseWriter, r *http.Request) {
	id, ok := s.sharePathID(w, r)
	if !ok {
		return
	}
	owner := CurrentUser(r.Context()).ID
	targetType, targetID, err := s.shares.Target(r.Context(), owner, id)
	if err != nil {
		s.shareError(w, r, "get", err)
		return
	}
	if err := s.shares.Revoke(r.Context(), owner, id); err != nil {
		s.shareError(w, r, "revoke", err)
		return
	}
	authRedirect(w, r, shareTargetPath(targetType, targetID))
}

func (s *Server) sharePathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id := parseID(r.PathValue("id"))
	if id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

func shareTargetPath(targetType share.TargetType, id int64) string {
	switch targetType {
	case share.TargetDay:
		return dayPath(id) + "#day-sharing"
	case share.TargetCollection:
		return "/collections/" + strconv.FormatInt(id, 10)
	default:
		return "/"
	}
}

func (s *Server) shareError(w http.ResponseWriter, r *http.Request, op string, err error) {
	switch {
	case errors.Is(err, share.ErrNotFound), errors.Is(err, share.ErrTargetInvalid):
		http.NotFound(w, r)
	case errors.Is(err, share.ErrRecipientsInvalid):
		s.authError(w, r, "sharing.error.recipients", http.StatusUnprocessableEntity)
	case errors.Is(err, share.ErrCannotShareSelf):
		s.authError(w, r, "sharing.error.self", http.StatusUnprocessableEntity)
	default:
		s.log.Error(op+" share", "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
	}
}

func (s *Server) hideSharedDay(w http.ResponseWriter, r *http.Request) {
	s.setSharedDayHidden(w, r, true)
}

func (s *Server) unhideSharedDay(w http.ResponseWriter, r *http.Request) {
	s.setSharedDayHidden(w, r, false)
}

func (s *Server) setSharedDayHidden(w http.ResponseWriter, r *http.Request, hidden bool) {
	id, ok := s.sharePathID(w, r)
	if !ok {
		return
	}
	if err := s.shares.SetHidden(r.Context(), CurrentUser(r.Context()).ID, id, hidden); err != nil {
		if errors.Is(err, share.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.shareError(w, r, "set hidden state", err)
		return
	}
	target := "/"
	if r.URL.Query().Get("from") == "shared" {
		target = "/shared"
	}
	authRedirect(w, r, target)
}

func (s *Server) handleSharedList(w http.ResponseWriter, r *http.Request) {
	u := CurrentUser(r.Context())
	list, err := s.shares.ListForUser(r.Context(), u.ID, true)
	if err != nil {
		s.shareError(w, r, "list shared days", err)
		return
	}
	daysForList := make([]days.Day, 0, len(list))
	byID := make(map[int64]share.SharedDay, len(list))
	for _, d := range list {
		daysForList = append(daysForList, sharedDayForList(d))
		byID[d.ID] = d
	}
	groups, err := s.loadGroupIndex(r, u.ID)
	if err != nil {
		s.dayFail(w, r, "list groups for shared days", err)
		return
	}
	now, loc := s.now(), userLocation(u)
	entries := append(days.HomeList(daysForList, days.WhenFuture, now, loc),
		days.HomeList(daysForList, days.WhenPast, now, loc)...)
	items := s.dayItems(r, entries, groups)
	for i := range items {
		sharedDay := byID[items[i].Day.ID]
		items[i].Shared, items[i].ShareOwner, items[i].Hidden = true, sharedDay.OwnerUsername, sharedDay.Hidden
		if sharedDay.Hidden {
			items[i].HideAction = "/shared/" + strconv.FormatInt(sharedDay.ID, 10) + "/unhide?from=shared"
		}
	}
	s.render(w, r, "shared.html", struct {
		pageData
		Items []dayItem
	}{newPageData(r), items})
}

type shareRow struct {
	Share          share.Share
	RecipientNames string
}

func shareRows(shares []share.Share) []shareRow {
	out := make([]shareRow, 0, len(shares))
	for _, sh := range shares {
		names := make([]string, 0, len(sh.Recipients))
		for _, recipient := range sh.Recipients {
			names = append(names, recipient.Username)
		}
		out = append(out, shareRow{Share: sh, RecipientNames: strings.Join(names, ", ")})
	}
	return out
}

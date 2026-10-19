package web

import (
	"errors"
	"net/http"

	"pandadays/internal/media"
)

type mediaPage struct {
	pageData
	Items []media.LibraryItem
	Total int64
	Item  media.LibraryItem
}

func (s *Server) registerMediaRoutes(mux *http.ServeMux) {
	mux.Handle("GET /media", RequireLogin(http.HandlerFunc(s.handleMediaLibrary)))
	mux.Handle("GET /media/{id}", RequireLogin(http.HandlerFunc(s.handleMediaDetail)))
}

func (s *Server) handleMediaDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := attachmentID(w, r, "id")
	if !ok {
		return
	}
	owner := CurrentUser(r.Context()).ID
	a, err := s.media.Get(r.Context(), owner, id)
	if errors.Is(err, media.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.attachmentError(w, r, "get media detail", err)
		return
	}
	d, err := s.days.Get(r.Context(), owner, a.DayID)
	if err != nil {
		s.dayFail(w, r, "get media source", err)
		return
	}
	s.render(w, r, "media.html", mediaPage{pageData: newPageData(r),
		Item: media.LibraryItem{Attachment: a, DayTitle: d.Title}})
}

func (s *Server) handleMediaLibrary(w http.ResponseWriter, r *http.Request) {
	items, err := s.media.ListOwner(r.Context(), CurrentUser(r.Context()).ID)
	if err != nil {
		s.attachmentError(w, r, "list library", err)
		return
	}
	page := mediaPage{pageData: newPageData(r), Items: items}
	for _, item := range items {
		page.Total += item.Size
	}
	s.render(w, r, "media.html", page)
}

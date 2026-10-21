package web

import (
	"errors"
	"net/http"
	"time"

	"pandadays/internal/media"
)

type mediaPage struct {
	pageData
	Items    []media.LibraryItem
	Total    int64
	Item     media.LibraryItem
	Groups   []mediaTimelineGroup
	GroupBy  string
	Kind     string
	HasMedia bool
}

type mediaTimelineGroup struct {
	Date  string
	Items []media.LibraryItem
}

func groupMedia(items []media.LibraryItem, groupBy string, loc *time.Location) []mediaTimelineGroup {
	format := "2006-01-02"
	if groupBy == "month" {
		format = "2006-01"
	}
	var groups []mediaTimelineGroup
	for _, item := range items {
		date := item.CreatedAt.In(loc).Format(format)
		if len(groups) == 0 || groups[len(groups)-1].Date != date {
			groups = append(groups, mediaTimelineGroup{Date: date})
		}
		group := &groups[len(groups)-1]
		group.Items = append(group.Items, item)
	}
	return groups
}

func (s *Server) registerMediaRoutes(mux *http.ServeMux) {
	mux.Handle("GET /media", RequireLogin(http.HandlerFunc(s.handleMediaLibrary)))
	mux.Handle("POST /media", RequireLogin(http.HandlerFunc(s.handleMediaUpload)))
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
	item := media.LibraryItem{Attachment: a}
	if a.DayID != 0 {
		d, err := s.days.Get(r.Context(), owner, a.DayID)
		if err != nil {
			s.dayFail(w, r, "get media source", err)
			return
		}
		item.DayTitle = d.Title
	}
	s.render(w, r, "media.html", mediaPage{pageData: newPageData(r),
		Item: item})
}

func (s *Server) handleMediaUpload(w http.ResponseWriter, r *http.Request) {
	if !s.limitLibraryForm(w, r) {
		return
	}
	if r.MultipartForm == nil {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			if r.MultipartForm != nil {
				r.MultipartForm.RemoveAll()
			}
			s.uploadReadError(w, r, err)
			return
		}
	}
	if r.MultipartForm == nil {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	owner := CurrentUser(r.Context()).ID
	remaining, err := s.media.LibraryRemaining(r.Context(), owner)
	if err != nil {
		s.attachmentError(w, r, "read library quota", err)
		return
	}
	for _, file := range files {
		if remaining <= 0 || file.Size > remaining {
			s.authError(w, r, "attachments.error.limit", http.StatusRequestEntityTooLarge)
			return
		}
		remaining -= file.Size
	}
	if err := s.checkUploadDuplicates(r.Context(), owner, files); err != nil {
		s.attachmentSaveError(w, r, "check library uploads", err)
		return
	}
	for _, header := range files {
		file, err := header.Open()
		if err == nil {
			_, err = s.media.UploadLibrary(r.Context(), owner, header.Filename, file)
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
		}
		if err != nil {
			s.attachmentSaveError(w, r, "upload library file", err)
			return
		}
	}
	authRedirect(w, r, "/media")
}

func (s *Server) handleMediaLibrary(w http.ResponseWriter, r *http.Request) {
	groupBy := r.URL.Query().Get("group")
	if groupBy == "" {
		groupBy = "day"
	}
	kind := r.URL.Query().Get("kind")
	if (groupBy != "day" && groupBy != "month") ||
		(kind != "" && kind != "image" && kind != "video" && kind != "audio" && kind != "file") {
		s.authError(w, r, "media.error.filter", http.StatusBadRequest)
		return
	}
	items, err := s.media.ListOwner(r.Context(), CurrentUser(r.Context()).ID)
	if err != nil {
		s.attachmentError(w, r, "list library", err)
		return
	}
	page := mediaPage{pageData: newPageData(r), GroupBy: groupBy, Kind: kind, HasMedia: len(items) > 0}
	for _, item := range items {
		if kind != "" && item.Kind != kind {
			continue
		}
		page.Items = append(page.Items, item)
		page.Total += item.Size
	}
	page.Groups = groupMedia(page.Items, groupBy, userLocation(CurrentUser(r.Context())))
	s.render(w, r, "media.html", page)
}

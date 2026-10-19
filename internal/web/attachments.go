package web

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"

	"pandadays/internal/media"
)

const multipartOverheadLimit = 64 << 10

func (s *Server) registerAttachmentRoutes(mux *http.ServeMux) {
	mux.Handle("POST /days/{id}/attachments", RequireLogin(http.HandlerFunc(s.handleAttachmentUpload)))
	mux.Handle("POST /days/{id}/attachments/{attachment_id}/background", RequireLogin(http.HandlerFunc(s.handleAttachmentBackground)))
	mux.Handle("POST /attachments/{id}/delete", RequireLogin(http.HandlerFunc(s.handleAttachmentDelete)))
	mux.Handle("GET /attachments/{id}", RequireLogin(http.HandlerFunc(s.handleAttachmentServe)))
}

func attachmentID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id := parseID(r.PathValue(name))
	if id <= 0 {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

func (s *Server) handleAttachmentUpload(w http.ResponseWriter, r *http.Request) {
	dayID, ok := attachmentID(w, r, "id")
	if !ok {
		return
	}
	ownerID := CurrentUser(r.Context()).ID
	remaining, err := s.media.Remaining(r.Context(), ownerID, dayID)
	if errors.Is(err, media.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.attachmentError(w, r, "check upload allowance", err)
		return
	}
	if remaining == 0 {
		s.authError(w, r, "attachments.error.limit", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, multipartBodyLimit(remaining))

	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
		files := r.MultipartForm.File["file"]
		if len(files) != 1 {
			s.authError(w, r, "error.bad_request", http.StatusBadRequest)
			return
		}
		file, err := files[0].Open()
		if err != nil {
			s.attachmentError(w, r, "open submitted attachment", err)
			return
		}
		defer file.Close()
		s.saveAttachment(w, r, ownerID, dayID, remaining, files[0].Filename, file)
		return
	}

	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			s.authError(w, r, "error.bad_request", http.StatusBadRequest)
			return
		}
		if err != nil {
			s.uploadReadError(w, r, err)
			return
		}
		if part.FormName() != "file" {
			if _, err := io.Copy(io.Discard, part); err != nil {
				s.uploadReadError(w, r, err)
				return
			}
			continue
		}
		filename := part.FileName()
		if filename == "" {
			part.Close()
			s.authError(w, r, "error.bad_request", http.StatusBadRequest)
			return
		}
		s.saveAttachment(w, r, ownerID, dayID, remaining, filename, part)
		part.Close()
		return
	}
}

func multipartBodyLimit(remaining int64) int64 {
	maxInt64 := int64(^uint64(0) >> 1)
	if remaining > maxInt64-multipartOverheadLimit {
		return maxInt64
	}
	return remaining + multipartOverheadLimit
}

func (s *Server) saveAttachment(w http.ResponseWriter, r *http.Request, ownerID, dayID, allowance int64, filename string, src io.Reader) {
	limited := http.MaxBytesReader(w, io.NopCloser(src), allowance)
	_, err := s.media.Upload(r.Context(), ownerID, dayID, filename, limited)
	if err != nil {
		if errors.Is(err, media.ErrQuota) || errors.Is(err, media.ErrTooLarge) {
			s.authError(w, r, "attachments.error.limit", http.StatusRequestEntityTooLarge)
			return
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			s.authError(w, r, "attachments.error.limit", http.StatusRequestEntityTooLarge)
			return
		}
		if errors.Is(err, media.ErrUploadStream) {
			s.authError(w, r, "error.bad_request", http.StatusBadRequest)
			return
		}
		if errors.Is(err, media.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		s.attachmentError(w, r, "upload", err)
		return
	}
	authRedirect(w, r, dayPath(dayID)+"#attachments")
}

func (s *Server) uploadReadError(w http.ResponseWriter, r *http.Request, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		s.authError(w, r, "attachments.error.limit", http.StatusRequestEntityTooLarge)
		return
	}
	s.authError(w, r, "error.bad_request", http.StatusBadRequest)
}

func (s *Server) handleAttachmentDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := attachmentID(w, r, "id")
	if !ok {
		return
	}
	ownerID := CurrentUser(r.Context()).ID
	attachment, err := s.media.Get(r.Context(), ownerID, id)
	if errors.Is(err, media.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.attachmentError(w, r, "get before delete", err)
		return
	}
	err = s.media.Delete(r.Context(), ownerID, id)
	switch {
	case errors.Is(err, media.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		s.attachmentError(w, r, "delete", err)
	default:
		authRedirect(w, r, dayPath(attachment.DayID)+"#attachments")
	}
}

func (s *Server) handleAttachmentBackground(w http.ResponseWriter, r *http.Request) {
	dayID, ok := attachmentID(w, r, "id")
	if !ok {
		return
	}
	attachmentID, ok := attachmentID(w, r, "attachment_id")
	if !ok {
		return
	}
	err := s.media.SetBackground(r.Context(), CurrentUser(r.Context()).ID, dayID, attachmentID)
	switch {
	case errors.Is(err, media.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		s.attachmentError(w, r, "set background", err)
	default:
		authRedirect(w, r, dayPath(dayID)+"#attachments")
	}
}

func (s *Server) handleAttachmentServe(w http.ResponseWriter, r *http.Request) {
	id, ok := attachmentID(w, r, "id")
	if !ok {
		return
	}
	ownerID := CurrentUser(r.Context()).ID
	a, err := s.media.GetAccessible(r.Context(), ownerID, id)
	if errors.Is(err, media.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.attachmentError(w, r, "get", err)
		return
	}
	f, err := os.Open(s.media.Path(a.OwnerID, a))
	if err != nil {
		s.attachmentError(w, r, "open", err)
		return
	}
	defer f.Close()

	disposition := "attachment"
	contentType := "application/octet-stream"
	if a.Kind == "image" || a.Kind == "audio" || a.Kind == "video" {
		disposition = "inline"
		contentType = a.MIME
	}
	contentDisposition := mime.FormatMediaType(disposition, map[string]string{"filename": a.Filename})
	if contentDisposition == "" {
		contentDisposition = disposition
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", contentDisposition)
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, a.Filename, a.CreatedAt, f)
}

func (s *Server) attachmentError(w http.ResponseWriter, r *http.Request, op string, err error) {
	s.log.Error(op+" attachment", "err", err)
	s.authError(w, r, "error.internal", http.StatusInternalServerError)
}

func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := "KMGTPE"
	value := float64(n)
	exp := -1
	for value >= 1024 && exp < len(units)-1 {
		value /= 1024
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", value, units[exp])
}

package web

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"os"

	"pandadays/internal/media"
)

const multipartOverheadLimit = 64 << 10

func (s *Server) registerAttachmentRoutes(mux *http.ServeMux) {
	mux.Handle("POST /days/{id}/attachments", RequireLogin(http.HandlerFunc(s.handleAttachmentUpload)))
	mux.Handle("POST /days/{id}/attachments/{attachment_id}/remove", RequireLogin(http.HandlerFunc(s.handleAttachmentRemove)))
	mux.Handle("POST /days/{id}/attachments/{attachment_id}/background", RequireLogin(http.HandlerFunc(s.handleAttachmentBackground)))
	mux.Handle("POST /attachments/{id}/delete", RequireLogin(http.HandlerFunc(s.handleAttachmentDelete)))
	mux.Handle("POST /attachments/{id}/rename", RequireLogin(http.HandlerFunc(s.handleAttachmentRename)))
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
	r.Body = http.MaxBytesReader(w, r.Body, multipartUploadLimit(remaining))
	if r.MultipartForm == nil {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			if r.MultipartForm != nil {
				r.MultipartForm.RemoveAll()
			}
			s.uploadReadError(w, r, err)
			return
		}
	}

	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
		files := r.MultipartForm.File["file"]
		form := parseDayForm(r)
		if len(files) == 0 && len(form.LibraryIDs) == 0 {
			s.authError(w, r, "error.bad_request", http.StatusBadRequest)
			return
		}
		prepared, err := s.prepareUploads(files)
		if err != nil {
			s.attachmentSaveError(w, r, "prepare selected attachments", err)
			return
		}
		defer func() {
			if err := closePreparedUploads(prepared); err != nil {
				s.log.Error("clean up prepared attachments", "err", err)
			}
		}()
		var library []media.Attachment
		for _, id := range form.LibraryIDs {
			a, err := s.media.Get(r.Context(), ownerID, id)
			if errors.Is(err, media.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			if err != nil {
				s.attachmentError(w, r, "read library selection", err)
				return
			}
			library = append(library, a)
		}
		allowance := remaining
		sizes := make([]int64, 0, len(library)+len(files))
		var active []media.Attachment
		if len(library) > 0 {
			active, err = s.media.ListDay(r.Context(), ownerID, dayID)
			if err != nil {
				s.attachmentError(w, r, "read current day attachments", err)
				return
			}
		}
		for _, a := range library {
			attached := false
			for _, current := range active {
				if current.ID == a.ID || (a.SHA256 != "" && current.SHA256 == a.SHA256) {
					attached = true
					break
				}
			}
			if !attached {
				sizes = append(sizes, a.Size)
			}
		}
		for _, file := range prepared {
			sizes = append(sizes, file.Size())
		}
		for _, size := range sizes {
			if size > allowance {
				s.authError(w, r, "attachments.error.limit", http.StatusRequestEntityTooLarge)
				return
			}
			allowance -= size
		}
		if err := s.checkUploadDuplicates(r.Context(), ownerID, prepared); err != nil {
			s.attachmentSaveError(w, r, "check selected attachments", err)
			return
		}
		for _, a := range library {
			if _, err := s.media.CopyToDay(r.Context(), ownerID, a.ID, dayID); err != nil {
				s.attachmentSaveError(w, r, "copy selected attachment", err)
				return
			}
		}
		for i, header := range files {
			_, err := s.media.UploadPrepared(r.Context(), ownerID, dayID, header.Filename, prepared[i])
			if err != nil {
				s.attachmentSaveError(w, r, "save selected attachment", err)
				return
			}
		}
		authRedirect(w, r, dayPath(dayID)+"#attachments")
		return
	}

	s.authError(w, r, "error.bad_request", http.StatusBadRequest)
}

func multipartBodyLimit(remaining int64) int64 {
	maxInt64 := int64(^uint64(0) >> 1)
	if remaining > maxInt64-multipartOverheadLimit {
		return maxInt64
	}
	return remaining + multipartOverheadLimit
}

func multipartUploadLimit(remaining int64) int64 {
	return multipartBodyLimit(max(remaining, media.MaxUploadBytes))
}

func (s *Server) prepareUploads(files []*multipart.FileHeader) ([]*media.PreparedUpload, error) {
	prepared := make([]*media.PreparedUpload, 0, len(files))
	for _, header := range files {
		file, err := header.Open()
		if err != nil {
			openErr := fmt.Errorf("%w: open upload: %w", media.ErrUploadStream, err)
			return nil, errors.Join(openErr, closePreparedUploads(prepared))
		}
		upload, prepareErr := s.media.PrepareUpload(file)
		closeErr := file.Close()
		if prepareErr == nil {
			prepareErr = closeErr
		}
		if prepareErr != nil {
			if upload != nil {
				if cleanupErr := upload.Close(); cleanupErr != nil {
					prepareErr = errors.Join(prepareErr, cleanupErr)
				}
			}
			if cleanupErr := closePreparedUploads(prepared); cleanupErr != nil {
				prepareErr = errors.Join(prepareErr, cleanupErr)
			}
			return nil, prepareErr
		}
		prepared = append(prepared, upload)
	}
	return prepared, nil
}

func closePreparedUploads(uploads []*media.PreparedUpload) error {
	var errs []error
	for _, upload := range uploads {
		if err := upload.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// checkUploadDuplicates rejects files already in the owner's media library, or
// the same file selected twice, before anything is saved. Upload repeats the
// library check transactionally.
func (s *Server) checkUploadDuplicates(ctx context.Context, ownerID int64, files []*media.PreparedUpload) error {
	seen := map[string]bool{}
	for _, file := range files {
		sum := file.SHA256()
		if seen[sum] {
			return &media.DuplicateError{}
		}
		seen[sum] = true
		existing, err := s.media.FindBySum(ctx, ownerID, sum)
		if err != nil {
			return err
		}
		if existing != 0 {
			return &media.DuplicateError{ExistingID: existing}
		}
	}
	return nil
}

func (s *Server) checkRawUploadDuplicates(ctx context.Context, ownerID int64, files []*multipart.FileHeader) error {
	seen := map[string]bool{}
	for _, header := range files {
		file, err := header.Open()
		if err != nil {
			return fmt.Errorf("%w: open upload: %w", media.ErrUploadStream, err)
		}
		sum, sumErr := media.Sum(file)
		closeErr := file.Close()
		if sumErr != nil {
			return fmt.Errorf("%w: hash upload: %w", media.ErrUploadStream, sumErr)
		}
		if closeErr != nil {
			return fmt.Errorf("%w: close upload: %w", media.ErrUploadStream, closeErr)
		}
		if seen[sum] {
			return &media.DuplicateError{}
		}
		seen[sum] = true
		existing, err := s.media.FindBySum(ctx, ownerID, sum)
		if err != nil {
			return err
		}
		if existing != 0 {
			return &media.DuplicateError{ExistingID: existing}
		}
	}
	return nil
}

func (s *Server) attachmentSaveError(w http.ResponseWriter, r *http.Request, op string, err error) {
	if errors.Is(err, media.ErrDuplicate) {
		s.authError(w, r, "attachments.error.duplicate", http.StatusConflict)
		return
	}
	if errors.Is(err, media.ErrQuota) || errors.Is(err, media.ErrTooLarge) || errors.Is(err, media.ErrImageTooLarge) {
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
	s.attachmentError(w, r, op, err)
}

func (s *Server) uploadReadError(w http.ResponseWriter, r *http.Request, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		s.authError(w, r, "attachments.error.limit", http.StatusRequestEntityTooLarge)
		return
	}
	s.authError(w, r, "error.bad_request", http.StatusBadRequest)
}

func (s *Server) handleAttachmentRemove(w http.ResponseWriter, r *http.Request) {
	dayID, ok := attachmentID(w, r, "id")
	if !ok {
		return
	}
	id, ok := attachmentID(w, r, "attachment_id")
	if !ok {
		return
	}
	err := s.media.Detach(r.Context(), CurrentUser(r.Context()).ID, dayID, id)
	switch {
	case errors.Is(err, media.ErrNotFound):
		http.NotFound(w, r)
	case err != nil:
		s.attachmentError(w, r, "remove from day", err)
	default:
		authRedirect(w, r, dayPath(dayID)+"#attachments")
	}
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
		if r.PostFormValue("return_to") == "/media" || attachment.DayID == 0 {
			authRedirect(w, r, "/media")
			return
		}
		authRedirect(w, r, dayPath(attachment.DayID)+"#attachments")
	}
}

func (s *Server) handleAttachmentRename(w http.ResponseWriter, r *http.Request) {
	id, ok := attachmentID(w, r, "id")
	if !ok {
		return
	}
	ownerID := CurrentUser(r.Context()).ID
	a, err := s.media.Get(r.Context(), ownerID, id)
	if err == nil {
		err = s.media.Rename(r.Context(), ownerID, id, r.PostFormValue("filename"))
	}
	switch {
	case errors.Is(err, media.ErrNotFound):
		http.NotFound(w, r)
	case errors.Is(err, media.ErrFilename):
		s.authError(w, r, "attachments.error.filename", http.StatusUnprocessableEntity)
	case err != nil:
		s.attachmentError(w, r, "rename", err)
	default:
		if r.PostFormValue("return_to") == "/media" || a.DayID == 0 {
			authRedirect(w, r, fmt.Sprintf("/media/%d", id))
			return
		}
		authRedirect(w, r, dayPath(a.DayID)+"#attachments")
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
	var err error
	if r.PostFormValue("clear") == "1" {
		err = s.media.ClearBackground(r.Context(), CurrentUser(r.Context()).ID, dayID, attachmentID)
	} else {
		err = s.media.SetBackground(r.Context(), CurrentUser(r.Context()).ID, dayID, attachmentID)
	}
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
	f, err := s.media.Open(a)
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
	w.Header().Set("Cache-Control", "private, no-store")
	if errors.Is(err, media.ErrFileMissing) {
		s.authError(w, r, "attachments.error.missing_file", http.StatusGone)
		return
	}
	if errors.Is(err, os.ErrPermission) {
		s.authError(w, r, "attachments.error.file_permission", http.StatusInternalServerError)
		return
	}
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

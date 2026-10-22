package web

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"

	"pandadays/internal/portability"
)

// maxImportErrors bounds the row errors listed on the preview page.
const maxImportErrors = 50

type importRowError struct {
	Line int
	Key  string
}

type importPage struct {
	pageData
	Preview    *portability.Preview
	Token      string
	Errors     []importRowError
	MoreErrors int
	Result     *portability.Result
}

func (s *Server) registerPortabilityRoutes(mux *http.ServeMux) {
	mux.Handle("GET /settings/export", RequireLogin(http.HandlerFunc(s.handleExport)))
	mux.Handle("POST /settings/import", RequireLogin(http.HandlerFunc(s.handleImportUpload)))
	mux.Handle("POST /settings/import/confirm", RequireLogin(http.HandlerFunc(s.handleImportConfirm)))
}

// trackingWriter records whether the response body has started, so export
// failures before the first byte can still produce an error page.
type trackingWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *trackingWriter) Write(p []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(p)
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	user := CurrentUser(r.Context())
	now := s.now()
	name := portability.ExportFilename(user.Username, now.In(userLocation(user)))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	tw := &trackingWriter{ResponseWriter: w}
	if err := s.portability.Export(r.Context(), user.ID, now, tw); err != nil {
		s.log.Error("export user data", "user_id", user.ID, "err", err)
		if !tw.wrote {
			w.Header().Del("Content-Disposition")
			s.authError(w, r, "error.internal", http.StatusInternalServerError)
		}
	}
}

func importErrorKey(err error) string {
	if key, ok := dayErrorKey(err); ok {
		return key
	}
	keys := []struct {
		err error
		key string
	}{
		{portability.ErrFieldCount, "import.error.field_count"},
		{portability.ErrArchivedInvalid, "import.error.archived"},
		{portability.ErrArchiveLive, "import.error.archive_live"},
		{portability.ErrCollectionName, "import.error.collection_name"},
		{portability.ErrTagName, "import.error.tag_name"},
		{portability.ErrAttachmentMissing, "import.error.attachment_missing"},
		{portability.ErrCardLimit, "import.error.card_limit"},
		{portability.ErrInvalidArchive, "import.error.invalid"},
		{portability.ErrUnsafeArchive, "import.error.unsafe"},
		{portability.ErrTooManyEntries, "import.error.entries"},
		{portability.ErrArchiveTooLarge, "import.error.too_large"},
		{portability.ErrMissingCSV, "import.error.missing_csv"},
		{portability.ErrInvalidCSV, "import.error.csv"},
		{portability.ErrStagedNotFound, "import.error.expired"},
		{portability.ErrQuota, "import.error.quota"},
	}
	for _, k := range keys {
		if errors.Is(err, k.err) {
			return k.key
		}
	}
	return ""
}

func rowErrorKey(err error) string {
	if key := importErrorKey(err); key != "" {
		return key
	}
	return "import.error.row"
}

// importFailed renders a known import problem on the settings page, or a
// generic error for anything unexpected.
func (s *Server) importFailed(w http.ResponseWriter, r *http.Request, op string, err error) {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		err = portability.ErrArchiveTooLarge
	}
	key := importErrorKey(err)
	if key == "" {
		s.log.Error(op, "user_id", CurrentUser(r.Context()).ID, "err", err)
		s.authError(w, r, "error.internal", http.StatusInternalServerError)
		return
	}
	status := http.StatusUnprocessableEntity
	if errors.Is(err, portability.ErrArchiveTooLarge) {
		status = http.StatusRequestEntityTooLarge
	}
	s.renderSettings(w, r, settingsPage{pageData: newPageData(r), Error: key, Status: status})
}

func (s *Server) handleImportUpload(w http.ResponseWriter, r *http.Request) {
	user := CurrentUser(r.Context())
	limit, err := s.portability.UploadLimit(r.Context(), user.ID)
	if err != nil {
		s.importFailed(w, r, "check import allowance", err)
		return
	}
	src, closeSrc, ok := s.importFile(w, r, limit)
	if !ok {
		return
	}
	token, err := s.portability.Stage(user.ID, src, limit, s.now())
	closeSrc()
	if err != nil {
		s.importFailed(w, r, "stage import", err)
		return
	}
	preview, err := s.portability.Preview(r.Context(), user.ID, token, s.now())
	if err != nil {
		s.portability.Discard(user.ID, token)
		s.importFailed(w, r, "preview import", err)
		return
	}
	page := importPage{pageData: newPageData(r), Preview: &preview, Token: token}
	for i, e := range preview.Errors {
		if i == maxImportErrors {
			page.MoreErrors = len(preview.Errors) - i
			break
		}
		page.Errors = append(page.Errors, importRowError{Line: e.Line, Key: rowErrorKey(e.Err)})
	}
	s.render(w, r, "import.html", page)
}

// importFile returns the uploaded "file" part, from the form parsed by the
// CSRF middleware or streamed when the token came in a header.
func (s *Server) importFile(w http.ResponseWriter, r *http.Request, limit int64) (io.Reader, func(), bool) {
	if r.MultipartForm != nil {
		files := r.MultipartForm.File["file"]
		if len(files) != 1 {
			r.MultipartForm.RemoveAll()
			s.authError(w, r, "error.bad_request", http.StatusBadRequest)
			return nil, nil, false
		}
		f, err := files[0].Open()
		if err != nil {
			r.MultipartForm.RemoveAll()
			s.importFailed(w, r, "open import upload", err)
			return nil, nil, false
		}
		return f, func() { f.Close(); r.MultipartForm.RemoveAll() }, true
	}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return nil, nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, multipartBodyLimit(limit))
	reader := multipart.NewReader(r.Body, params["boundary"])
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			s.authError(w, r, "error.bad_request", http.StatusBadRequest)
			return nil, nil, false
		}
		if err != nil {
			s.uploadReadError(w, r, err)
			return nil, nil, false
		}
		if part.FormName() == "file" && part.FileName() != "" {
			return part, func() { part.Close() }, true
		}
		if _, err := io.Copy(io.Discard, part); err != nil {
			s.uploadReadError(w, r, err)
			return nil, nil, false
		}
	}
}

func (s *Server) handleImportConfirm(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	user := CurrentUser(r.Context())
	result, err := s.portability.Import(r.Context(), user.ID, r.PostForm.Get("token"), s.now())
	if err != nil {
		s.importFailed(w, r, "import user data", err)
		return
	}
	s.render(w, r, "import.html", importPage{pageData: newPageData(r), Result: &result})
}

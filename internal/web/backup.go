package web

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"pandadays/internal/auth"
	"pandadays/internal/backup"
	"pandadays/internal/db"
)

type backupPage struct {
	pageData
	Files       []backup.Retained
	Retention   int64
	RestoreName string
}

func (s *Server) registerBackupRoutes(mux *http.ServeMux) {
	mux.Handle("GET /admin/backup", RequireAdmin(http.HandlerFunc(s.handleBackup)))
	mux.Handle("POST /admin/backup", RequireAdmin(http.HandlerFunc(s.createBackup)))
	mux.Handle("GET /admin/backup/{name}", RequireAdmin(http.HandlerFunc(s.downloadBackup)))
	mux.Handle("POST /admin/backup/{name}/delete", RequireAdmin(http.HandlerFunc(s.deleteBackup)))
	mux.Handle("GET /admin/backup/restore", RequireAdmin(http.HandlerFunc(s.handleRestoreForm)))
	mux.Handle("GET /admin/backup/{name}/restore", RequireAdmin(http.HandlerFunc(s.handleRestoreForm)))
	mux.Handle("POST /admin/backup/{name}/restore", RequireAdmin(http.HandlerFunc(s.restoreBackup)))
	mux.Handle("POST /admin/backup/restore", RequireAdmin(http.HandlerFunc(s.restoreBackup)))
}

func (s *Server) backupError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("backup operation", "err", err)
	s.authError(w, r, "backup.error.failed", http.StatusInternalServerError)
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	settings, err := s.auth.SiteSettings(r.Context())
	if err != nil {
		s.backupError(w, r, err)
		return
	}
	files, err := s.backup.List()
	if err != nil {
		s.backupError(w, r, err)
		return
	}
	s.render(w, r, "backup.html", backupPage{pageData: newPageData(r), Files: files, Retention: settings.BackupRetention})
}

func (s *Server) handleRestoreForm(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name != "" {
		file, err := s.backup.Open(name)
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.backupError(w, r, err)
			return
		}
		if err := file.Close(); err != nil {
			s.backupError(w, r, err)
			return
		}
	}
	s.render(w, r, "backup_restore.html", backupPage{pageData: newPageData(r), RestoreName: name})
}

func (s *Server) sendBackup(w http.ResponseWriter, r *http.Request, file *os.File, name string) {
	info, err := file.Stat()
	if err != nil {
		s.backupError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	file, err := s.backup.Open(r.PathValue("name"))
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.backupError(w, r, err)
		return
	}
	defer file.Close()
	s.sendBackup(w, r, file, r.PathValue("name"))
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "backup.error.upload", http.StatusBadRequest)
		return
	}
	if r.PostForm.Get("confirm") != "1" {
		s.authError(w, r, "admin.error.confirm", http.StatusUnprocessableEntity)
		return
	}
	if err := s.backup.Delete(r.PathValue("name")); errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.backupError(w, r, err)
		return
	}
	http.Redirect(w, r, "/admin/backup", http.StatusSeeOther)
}

func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "backup.error.upload", http.StatusBadRequest)
		return
	}
	if !s.beginMaintenance(w, r) {
		return
	}
	defer s.endMaintenance()
	settings, err := s.auth.SiteSettings(r.Context())
	if err != nil {
		s.backupError(w, r, err)
		return
	}
	if r.PostForm.Get("mode") == "list" && settings.BackupRetention == 0 {
		s.authError(w, r, "backup.error.retention", http.StatusUnprocessableEntity)
		return
	}
	tmp := filepath.Join(s.cfg.DataDir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		s.backupError(w, r, err)
		return
	}
	file, err := os.CreateTemp(tmp, "backup-download-*")
	if err != nil {
		s.backupError(w, r, err)
		return
	}
	filename := file.Name()
	retained := false
	defer func() {
		if !retained {
			if err := os.Remove(filename); err != nil && !errors.Is(err, os.ErrNotExist) {
				s.log.Error("remove backup download", "err", err)
			}
		}
	}()
	err = s.backup.Create(r.Context(), s.db, file, s.now())
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil {
		s.backupError(w, r, err)
		return
	}
	name := "pandadays-backup-" + s.now().UTC().Format("20060102T150405Z") + "-" + filepath.Base(filename)[len("backup-download-"):] + ".tar.gz"
	if (r.PostForm.Get("retain") == "1" || r.PostForm.Get("mode") == "list") && settings.BackupRetention > 0 {
		directory := filepath.Join(s.cfg.DataDir, "backups")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			s.backupError(w, r, err)
			return
		}
		target := filepath.Join(directory, name)
		if err := os.Rename(filename, target); err != nil {
			s.backupError(w, r, err)
			return
		}
		filename = target
		retained = true
	}
	file, err = os.Open(filename)
	if err != nil {
		s.backupError(w, r, err)
		return
	}
	defer file.Close()
	if err := s.backup.Prune(settings.BackupRetention); err != nil {
		s.backupError(w, r, err)
		return
	}
	if r.PostForm.Get("mode") == "list" {
		http.Redirect(w, r, "/admin/backup", http.StatusSeeOther)
		return
	}
	s.sendBackup(w, r, file, name)
}

func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.authError(w, r, "error.bad_request", http.StatusBadRequest)
		return
	}
	user := CurrentUser(r.Context())
	verified, err := s.auth.Authenticate(r.Context(), ClientIP(r), user.Username, r.PostForm.Get("password"), s.now())
	if errors.Is(err, auth.ErrCredentials) || errors.Is(err, auth.ErrRateLimited) {
		status := http.StatusUnprocessableEntity
		if errors.Is(err, auth.ErrRateLimited) {
			status = http.StatusTooManyRequests
		}
		s.authError(w, r, "backup.error.password", status)
		return
	}
	if err != nil {
		s.backupError(w, r, err)
		return
	}
	if verified.ID != user.ID || !verified.IsAdmin {
		http.NotFound(w, r)
		return
	}
	filename, _ := r.Context().Value(restoreUploadKey{}).(string)
	var file *os.File
	if name := r.PathValue("name"); name != "" {
		file, err = s.backup.Open(name)
	} else {
		file, err = os.Open(filename)
	}
	if errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.backupError(w, r, err)
		return
	}
	defer file.Close()
	if _, err := s.backup.Validate(r.Context(), file); err != nil {
		s.log.Warn("validate restore archive", "err", err)
		s.authError(w, r, "backup.error.archive", http.StatusUnprocessableEntity)
		return
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		s.backupError(w, r, err)
		return
	}
	if !s.beginMaintenance(w, r) {
		return
	}
	// Complete recovery even if the uploading browser disconnects.
	ctx := context.WithoutCancel(r.Context())
	restoreErr := s.db.Close()
	if restoreErr == nil {
		restoreErr = s.backup.Restore(ctx, file, s.now())
	}
	if errors.Is(restoreErr, backup.ErrRollback) {
		s.log.Error("restore rollback failed; maintenance remains enabled", "err", restoreErr)
		s.maintenanceResponse(w, r, s.maintenanceLang)
		return
	}
	path := filepath.Join(s.cfg.DataDir, db.FileName)
	info, reopenErr := os.Lstat(path)
	var conn *sql.DB
	if reopenErr == nil && !info.Mode().IsRegular() {
		reopenErr = fmt.Errorf("live database is not a regular file")
	}
	if reopenErr == nil {
		conn, reopenErr = db.Open(ctx, path)
	}
	if reopenErr == nil {
		reopenErr = s.bindDatabase(conn)
		if reopenErr != nil {
			reopenErr = errors.Join(reopenErr, conn.Close())
		}
	}
	if reopenErr != nil {
		s.log.Error("reopen live database; maintenance remains enabled", "err", errors.Join(restoreErr, reopenErr))
		s.maintenanceResponse(w, r, s.maintenanceLang)
		return
	}
	s.handler = s.routes()
	defer s.endMaintenance()
	if restoreErr != nil {
		s.backupError(w, r, restoreErr)
		return
	}
	auth.ClearSessionCookie(w, s.cfg.CookieSecure)
	authRedirect(w, r, "/login")
}

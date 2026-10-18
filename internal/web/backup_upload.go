package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

type restoreUploadKey struct{}

// stageRestoreForm streams the file to the data volume without a payload size
// limit. Only the small text fields are buffered, regardless of part order.
func (s *Server) stageRestoreForm(r *http.Request) (filename string, err error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil && filename != "" {
			err = errors.Join(err, os.Remove(filename))
		}
	}()
	r.PostForm = make(url.Values)
	count := 0
	for {
		part, readErr := reader.NextPart()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return filename, readErr
		}
		count++
		if count > 3 {
			return filename, fmt.Errorf("too many restore form parts")
		}
		name := part.FormName()
		if name == "file" && part.FileName() != "" && filename == "" {
			tmp := filepath.Join(s.cfg.DataDir, "tmp")
			if err := os.MkdirAll(tmp, 0o700); err != nil {
				return filename, err
			}
			file, err := os.CreateTemp(tmp, "restore-upload-*")
			if err != nil {
				return filename, err
			}
			filename = file.Name()
			_, copyErr := io.Copy(file, part)
			if err := errors.Join(copyErr, file.Close()); err != nil {
				return filename, err
			}
		} else if (name == "password" || name == "csrf_token") && part.FileName() == "" && !r.PostForm.Has(name) {
			value, err := io.ReadAll(io.LimitReader(part, (64<<10)+1))
			if err != nil {
				return filename, err
			}
			if len(value) > 64<<10 {
				return filename, fmt.Errorf("restore field too large")
			}
			r.PostForm.Set(name, string(value))
		} else {
			return filename, fmt.Errorf("unexpected restore form part")
		}
	}
	if filename == "" {
		return "", fmt.Errorf("missing restore archive")
	}
	r.Form = r.PostForm
	*r = *r.WithContext(context.WithValue(r.Context(), restoreUploadKey{}, filename))
	return filename, nil
}

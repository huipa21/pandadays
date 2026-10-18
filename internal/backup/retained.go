package backup

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var retainedName = regexp.MustCompile(`^(pandadays-backup|pre-restore)-[A-Za-z0-9-]+\.tar\.gz$`)

type Retained struct {
	Name     string
	Size     int64
	Modified time.Time
}

func (s *Service) List() ([]Retained, error) {
	entries, err := os.ReadDir(filepath.Join(s.dataDir, "backups"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var files []Retained
	for _, entry := range entries {
		if !retainedName.MatchString(entry.Name()) || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		files = append(files, Retained{entry.Name(), info.Size(), info.ModTime()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Modified.Equal(files[j].Modified) {
			return files[i].Name > files[j].Name
		}
		return files[i].Modified.After(files[j].Modified)
	})
	return files, nil
}

func (s *Service) retainedPath(name string) (string, error) {
	if !retainedName.MatchString(name) {
		return "", os.ErrNotExist
	}
	path := filepath.Join(s.dataDir, "backups", name)
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", os.ErrNotExist
	}
	return path, nil
}

func (s *Service) Open(name string) (*os.File, error) {
	path, err := s.retainedPath(name)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func (s *Service) Delete(name string) error {
	path, err := s.retainedPath(name)
	if err != nil {
		return err
	}
	return os.Remove(path)
}

// Prune removes only ordinary backups; recovery snapshots need explicit deletion.
func (s *Service) Prune(keep int64) error {
	files, err := s.List()
	if err != nil {
		return err
	}
	for _, file := range files {
		if strings.HasPrefix(file.Name, "pre-restore-") {
			continue
		}
		if keep > 0 {
			keep--
			continue
		}
		if err := s.Delete(file.Name); err != nil {
			return err
		}
	}
	return nil
}

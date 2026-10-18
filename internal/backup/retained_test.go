package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRetainedPruningAndSafePaths(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "backups")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	service := New(root, "test")
	names := []string{
		"pandadays-backup-old.tar.gz",
		"pandadays-backup-new.tar.gz",
		"pre-restore-safety.tar.gz",
		"unrelated.txt",
	}
	for i, name := range names {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		modified := time.Unix(int64(i), 0)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(directory, "pandadays-backup-directory.tar.gz"), 0o700); err != nil {
		t.Fatal(err)
	}
	files, err := service.List()
	if err != nil || len(files) != 3 {
		t.Fatalf("list: %+v %v", files, err)
	}
	if err := service.Prune(1); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Open(names[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old backup retained: %v", err)
	}
	if err := service.Prune(0); err != nil {
		t.Fatal(err)
	}
	files, err = service.List()
	if err != nil || len(files) != 1 || files[0].Name != names[2] {
		t.Fatalf("recovery snapshot pruned: %+v %v", files, err)
	}
	for _, name := range []string{"../unrelated.txt", `..\unrelated.txt`, "C:secret", "unrelated.txt", "pandadays-backup-directory.tar.gz"} {
		if file, err := service.Open(name); !errors.Is(err, os.ErrNotExist) {
			if file != nil {
				file.Close()
			}
			t.Fatalf("unsafe open %q: %v", name, err)
		}
		if err := service.Delete(name); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsafe delete %q: %v", name, err)
		}
	}
	if err := service.Delete(names[2]); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, names[3])); err != nil {
		t.Fatalf("unrelated file removed: %v", err)
	}
}

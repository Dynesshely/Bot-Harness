package store

import (
	"os"
	"path/filepath"
	"testing"
)

// The SQLite database carries chat history and the admin token. A fresh file
// must not be group/world readable just because the process umask is 022.
func TestOpenRestrictsSQLiteFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bot.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = s.Close() }()

	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Stat(candidate)
		if os.IsNotExist(err) {
			continue // WAL/SHM may not exist until the first write.
		}
		if err != nil {
			t.Fatalf("stat %s: %v", candidate, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode = %o, want 600", candidate, got)
		}
	}
}

//go:build windows

package selfupdate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplaceExecutableWindows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wln.exe")
	if err := os.WriteFile(path, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	deferred, err := replaceExecutable(path, []byte("new"))
	if err != nil {
		t.Fatal(err)
	}
	if !deferred {
		t.Fatal("Windows replacement was not deferred")
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Equal(data, []byte("new")) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("replacement helper did not update %s", path)
}

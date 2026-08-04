//go:build !windows

package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
)

func replaceExecutable(path string, data []byte) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("inspect current executable: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".wln-update-*")
	if err != nil {
		return false, fmt.Errorf("create update beside executable: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	mode := info.Mode().Perm()
	if mode&0o111 == 0 {
		mode = 0o755
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return false, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	if err := os.Rename(name, path); err != nil {
		return false, fmt.Errorf("replace executable: %w", err)
	}
	return false, nil
}

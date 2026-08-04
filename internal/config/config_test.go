package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSaveLoadAndResolve(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Profile{Server: "https://example.test", Token: "secret", OperateAs: "subuser"}
	cfg := &File{DefaultProfile: "prod", Profiles: map[string]Profile{"prod": want}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("config permissions = %o, want 600", got)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	name, got, err := loaded.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if name != "prod" || got != want {
		t.Fatalf("resolved = %q %#v, want prod %#v", name, got, want)
	}
}

func TestLoadMissingReturnsEmptyConfig(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 0 {
		t.Fatalf("profiles = %d, want 0", len(cfg.Profiles))
	}
}

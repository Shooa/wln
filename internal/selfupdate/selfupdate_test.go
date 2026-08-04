package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestVersionComparison(t *testing.T) {
	tests := []struct {
		left, right string
		want        int
	}{
		{"1.2.3", "1.2.3", 0},
		{"v1.2.4", "1.2.3", 1},
		{"1.10.0", "1.9.9", 1},
		{"1.2.2", "1.2.3", -1},
		{"dev", "1.2.3", -1},
		{"1.2.3", "dev", 1},
	}
	for _, tt := range tests {
		if got := compareVersions(tt.left, tt.right); got != tt.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tt.left, tt.right, got, tt.want)
		}
	}
}

func TestCheckUsesFreshCache(t *testing.T) {
	restore := saveGlobals()
	defer restore()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_ = json.NewEncoder(w).Encode(Release{TagName: "v1.2.0", HTMLURL: "https://example.test/release"})
	}))
	defer server.Close()
	apiURL = server.URL
	cacheFilePath := filepath.Join(t.TempDir(), "cache", "update.json")
	cachePath = func() (string, error) { return cacheFilePath, nil }
	fixed := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }

	first, err := Check(context.Background(), "1.1.0", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Check(context.Background(), "1.1.0", false)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Available || !second.Available || first.LatestVersion != "1.2.0" {
		t.Fatalf("unexpected check results: %#v %#v", first, second)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}

	now = func() time.Time { return fixed.Add(checkInterval) }
	if _, err := Check(context.Background(), "1.1.0", false); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests after expiry = %d, want 2", requests)
	}
}

func TestCheckCachesAutomaticFailure(t *testing.T) {
	restore := saveGlobals()
	defer restore()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	apiURL = server.URL
	cacheFilePath := filepath.Join(t.TempDir(), "update.json")
	cachePath = func() (string, error) { return cacheFilePath, nil }
	now = func() time.Time { return time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC) }

	if _, err := Check(context.Background(), "1.1.0", false); err == nil {
		t.Fatal("first check unexpectedly succeeded")
	}
	info, err := Check(context.Background(), "1.1.0", false)
	if err != nil {
		t.Fatalf("cached failure returned error: %v", err)
	}
	if info.Available || requests != 1 {
		t.Fatalf("info = %#v, requests = %d", info, requests)
	}
}

func TestDownloadVerifiedBinary(t *testing.T) {
	restore := saveGlobals()
	defer restore()
	runtimeGOOS, runtimeGOARCH = "linux", "amd64"
	binary := []byte("new executable")
	archive := makeTarGZ(t, "wln", binary)
	name := "wln_1.2.3_linux_amd64.tar.gz"
	release, closeServer := serveReleaseAssets(t, "v1.2.3", name, archive, false)
	defer closeServer()
	got, err := downloadVerifiedBinary(context.Background(), release)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("binary = %q", got)
	}
}

func TestDownloadVerifiedWindowsBinary(t *testing.T) {
	restore := saveGlobals()
	defer restore()
	runtimeGOOS, runtimeGOARCH = "windows", "arm64"
	binary := []byte("windows executable")
	archive := makeZip(t, "wln.exe", binary)
	name := "wln_1.2.3_windows_arm64.zip"
	release, closeServer := serveReleaseAssets(t, "v1.2.3", name, archive, false)
	defer closeServer()
	got, err := downloadVerifiedBinary(context.Background(), release)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, binary) {
		t.Fatalf("binary = %q", got)
	}
}

func TestDownloadRejectsBadChecksum(t *testing.T) {
	restore := saveGlobals()
	defer restore()
	runtimeGOOS, runtimeGOARCH = "linux", "amd64"
	archive := makeTarGZ(t, "wln", []byte("new executable"))
	name := "wln_1.2.3_linux_amd64.tar.gz"
	release, closeServer := serveReleaseAssets(t, "v1.2.3", name, archive, true)
	defer closeServer()
	_, err := downloadVerifiedBinary(context.Background(), release)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("error = %v", err)
	}
}

func TestUpdateReplacesUnixExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix replacement semantics")
	}
	restore := saveGlobals()
	defer restore()
	runtimeGOOS, runtimeGOARCH = runtime.GOOS, runtime.GOARCH
	newBinary := []byte("new executable")
	archive := makeTarGZ(t, "wln", newBinary)
	name := fmt.Sprintf("wln_1.2.3_%s_%s.tar.gz", runtimeGOOS, runtimeGOARCH)
	release, closeServer := serveReleaseAssets(t, "v1.2.3", name, archive, false)
	defer closeServer()
	apiURL = releaseAPIURL(release)
	current := filepath.Join(t.TempDir(), "wln")
	if err := os.WriteFile(current, []byte("old executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	executable = func() (string, error) { return current, nil }
	cachePath = func() (string, error) { return filepath.Join(t.TempDir(), "cache"), nil }
	result, err := Update(context.Background(), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != "1.2.3" || result.Deferred {
		t.Fatalf("result = %#v", result)
	}
	got, err := os.ReadFile(current)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newBinary) {
		t.Fatalf("installed binary = %q", got)
	}
}

func TestChecksumFor(t *testing.T) {
	data := []byte("abc123  ./one.tar.gz\n" + strings.Repeat("a", 64) + "  ./two.tar.gz\n")
	got, err := checksumFor(data, "two.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if got != strings.Repeat("a", 64) {
		t.Fatalf("checksum = %q", got)
	}
}

func serveReleaseAssets(t *testing.T, tag, archiveName string, archive []byte, badChecksum bool) (Release, func()) {
	t.Helper()
	sum := sha256.Sum256(archive)
	checksum := hex.EncodeToString(sum[:])
	if badChecksum {
		checksum = strings.Repeat("0", 64)
	}
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	release := Release{
		TagName: tag,
		HTMLURL: server.URL + "/release",
		Assets: []Asset{
			{Name: archiveName, BrowserDownloadURL: server.URL + "/archive"},
			{Name: "SHA256SUMS", BrowserDownloadURL: server.URL + "/sums"},
		},
	}
	mux.HandleFunc("/archive", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/sums", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  ./%s\n", checksum, archiveName)
	})
	mux.HandleFunc("/latest", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(release) })
	return release, server.Close
}

func releaseAPIURL(release Release) string {
	return strings.TrimSuffix(release.HTMLURL, "/release") + "/latest"
}

func makeTarGZ(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func makeZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)
	entry, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func saveGlobals() func() {
	oldAPIURL, oldCachePath, oldExecutable, oldNow := apiURL, cachePath, executable, now
	oldGOOS, oldGOARCH := runtimeGOOS, runtimeGOARCH
	return func() {
		apiURL, cachePath, executable, now = oldAPIURL, oldCachePath, oldExecutable, oldNow
		runtimeGOOS, runtimeGOARCH = oldGOOS, oldGOARCH
	}
}

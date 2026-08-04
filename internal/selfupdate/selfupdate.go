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
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIURL = "https://api.github.com/repos/Shooa/wln/releases/latest"
	checkInterval = 24 * time.Hour
	maxAssetBytes = 128 << 20
)

var (
	apiURL        = defaultAPIURL
	cachePath     = defaultCachePath
	executable    = os.Executable
	now           = time.Now
	runtimeGOOS   = runtime.GOOS
	runtimeGOARCH = runtime.GOARCH
)

type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type Release struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

type Info struct {
	CurrentVersion string
	LatestVersion  string
	URL            string
	Available      bool
	Release        Release
}

type Result struct {
	Version  string
	Deferred bool
}

type cacheFile struct {
	CheckedAt time.Time `json:"checked_at"`
	Release   Release   `json:"release"`
	Failed    bool      `json:"failed,omitempty"`
}

func Check(ctx context.Context, currentVersion string, force bool) (Info, error) {
	currentVersion = normalizeVersion(currentVersion)
	if !force {
		if cached, ok := loadFreshCache(now()); ok {
			return releaseInfo(currentVersion, cached), nil
		}
	}
	release, err := fetchRelease(ctx)
	if err != nil {
		if !force {
			_ = saveCache(cacheFile{CheckedAt: now(), Failed: true})
		}
		return Info{}, err
	}
	_ = saveCache(cacheFile{CheckedAt: now(), Release: release})
	return releaseInfo(currentVersion, release), nil
}

func IsReleaseVersion(value string) bool {
	return isReleaseVersion(value)
}

func Update(ctx context.Context, currentVersion string) (Result, error) {
	info, err := Check(ctx, currentVersion, true)
	if err != nil {
		return Result{}, err
	}
	if !info.Available && isReleaseVersion(currentVersion) {
		return Result{Version: normalizeVersion(currentVersion)}, nil
	}
	data, err := downloadVerifiedBinary(ctx, info.Release)
	if err != nil {
		return Result{}, err
	}
	path, err := executable()
	if err != nil {
		return Result{}, fmt.Errorf("locate executable: %w", err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return Result{}, fmt.Errorf("resolve executable: %w", err)
	}
	deferred, err := replaceExecutable(path, data)
	if err != nil {
		return Result{}, err
	}
	return Result{Version: info.LatestVersion, Deferred: deferred}, nil
}

func releaseInfo(current string, release Release) Info {
	latest := normalizeVersion(release.TagName)
	return Info{
		CurrentVersion: current,
		LatestVersion:  latest,
		URL:            release.HTMLURL,
		Available:      compareVersions(latest, current) > 0 || !isReleaseVersion(current),
		Release:        release,
	}
}

func fetchRelease(ctx context.Context) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "wln-updater")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("check latest release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("check latest release: GitHub returned %s", resp.Status)
	}
	var release Release
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 2<<20))
	if err := decoder.Decode(&release); err != nil {
		return Release{}, fmt.Errorf("decode latest release: %w", err)
	}
	if !isReleaseVersion(release.TagName) {
		return Release{}, fmt.Errorf("latest release has invalid tag %q", release.TagName)
	}
	return release, nil
}

func downloadVerifiedBinary(ctx context.Context, release Release) ([]byte, error) {
	version := normalizeVersion(release.TagName)
	extension := ".tar.gz"
	binaryName := "wln"
	if runtimeGOOS == "windows" {
		extension = ".zip"
		binaryName = "wln.exe"
	}
	archiveName := fmt.Sprintf("wln_%s_%s_%s%s", version, runtimeGOOS, runtimeGOARCH, extension)
	archiveURL, sumsURL := "", ""
	for _, asset := range release.Assets {
		switch asset.Name {
		case archiveName:
			archiveURL = asset.BrowserDownloadURL
		case "SHA256SUMS":
			sumsURL = asset.BrowserDownloadURL
		}
	}
	if archiveURL == "" {
		return nil, fmt.Errorf("release %s has no asset for %s/%s", release.TagName, runtimeGOOS, runtimeGOARCH)
	}
	if sumsURL == "" {
		return nil, errors.New("release has no SHA256SUMS asset")
	}
	sums, err := download(ctx, sumsURL, 1<<20)
	if err != nil {
		return nil, fmt.Errorf("download checksums: %w", err)
	}
	want, err := checksumFor(sums, archiveName)
	if err != nil {
		return nil, err
	}
	archive, err := download(ctx, archiveURL, maxAssetBytes)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", archiveName, err)
	}
	got := sha256.Sum256(archive)
	if !strings.EqualFold(hex.EncodeToString(got[:]), want) {
		return nil, fmt.Errorf("checksum mismatch for %s", archiveName)
	}
	if extension == ".zip" {
		return extractZip(archive, binaryName)
	}
	return extractTarGZ(archive, binaryName)
}

func download(ctx context.Context, target string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "wln-updater")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %s", resp.Status)
	}
	reader := io.LimitReader(resp.Body, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("download exceeds %d bytes", limit)
	}
	return data, nil
}

func checksumFor(data []byte, name string) (string, error) {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		candidate := strings.TrimPrefix(strings.TrimPrefix(fields[1], "*"), "./")
		if candidate == name {
			if len(fields[0]) != sha256.Size*2 {
				return "", fmt.Errorf("invalid checksum for %s", name)
			}
			if _, err := hex.DecodeString(fields[0]); err != nil {
				return "", fmt.Errorf("invalid checksum for %s", name)
			}
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS has no entry for %s", name)
}

func extractTarGZ(data []byte, binaryName string) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	defer gz.Close()
	tarReader := tar.NewReader(gz)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release archive: %w", err)
		}
		if filepath.Base(header.Name) == binaryName && header.Typeflag == tar.TypeReg {
			if header.Size > maxAssetBytes {
				return nil, errors.New("executable in release archive is too large")
			}
			return io.ReadAll(io.LimitReader(tarReader, maxAssetBytes+1))
		}
	}
	return nil, fmt.Errorf("release archive does not contain %s", binaryName)
}

func extractZip(data []byte, binaryName string) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open release archive: %w", err)
	}
	for _, file := range reader.File {
		if filepath.Base(file.Name) != binaryName || file.FileInfo().IsDir() {
			continue
		}
		if file.UncompressedSize64 > maxAssetBytes {
			return nil, errors.New("executable in release archive is too large")
		}
		opened, err := file.Open()
		if err != nil {
			return nil, err
		}
		content, readErr := io.ReadAll(io.LimitReader(opened, maxAssetBytes+1))
		closeErr := opened.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(content) > maxAssetBytes {
			return nil, errors.New("executable in release archive is too large")
		}
		return content, nil
	}
	return nil, fmt.Errorf("release archive does not contain %s", binaryName)
}

func normalizeVersion(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "v")
}

func isReleaseVersion(value string) bool {
	parts := strings.Split(normalizeVersion(value), ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if _, err := strconv.ParseUint(part, 10, 64); err != nil {
			return false
		}
	}
	return true
}

func compareVersions(left, right string) int {
	if !isReleaseVersion(left) {
		return -1
	}
	if !isReleaseVersion(right) {
		return 1
	}
	lp, rp := strings.Split(normalizeVersion(left), "."), strings.Split(normalizeVersion(right), ".")
	for i := range lp {
		ln, _ := strconv.ParseUint(lp[i], 10, 64)
		rn, _ := strconv.ParseUint(rp[i], 10, 64)
		if ln < rn {
			return -1
		}
		if ln > rn {
			return 1
		}
	}
	return 0
}

func defaultCachePath() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wln", "update-check.json"), nil
}

func loadFreshCache(at time.Time) (Release, bool) {
	path, err := cachePath()
	if err != nil {
		return Release{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Release{}, false
	}
	var cached cacheFile
	if json.Unmarshal(data, &cached) != nil || (cached.Release.TagName == "" && !cached.Failed) || at.Sub(cached.CheckedAt) >= checkInterval || at.Before(cached.CheckedAt) {
		return Release{}, false
	}
	return cached.Release, true
}

func saveCache(cached cacheFile) error {
	path, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(cached)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".update-check-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

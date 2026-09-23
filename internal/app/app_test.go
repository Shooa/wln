package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shooa/wln/internal/config"
	"github.com/Shooa/wln/internal/wialon"
)

func TestMessagesGetEndToEndWithPagination(t *testing.T) {
	var mu sync.Mutex
	var services []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		service := r.Form.Get("svc")
		mu.Lock()
		services = append(services, service)
		mu.Unlock()
		switch service {
		case "token/login":
			_, _ = w.Write([]byte(`{"eid":"test-session"}`))
		case "core/search_items":
			_, _ = w.Write([]byte(`{"items":[{"id":1001,"nm":"Test unit","uid":"123456789012345","hw":1}]}`))
		case "core/search_item":
			_, _ = w.Write([]byte(`{"item":null}`))
		case "messages/load_interval":
			_, _ = w.Write([]byte(`{"count":3,"messages":[` + messageJSON(1, 100) + `,` + messageJSON(2, 101) + `]}`))
		case "messages/get_messages":
			var params map[string]any
			if err := json.Unmarshal([]byte(r.Form.Get("params")), &params); err != nil {
				t.Fatal(err)
			}
			if params["indexFrom"] != float64(2) || params["indexTo"] != float64(3) {
				t.Errorf("pagination params = %#v", params)
			}
			_, _ = w.Write([]byte(`[` + messageJSON(3, 102) + `]`))
		case "messages/unload":
			_, _ = w.Write([]byte(`{}`))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", service)
		}
	}))
	defer server.Close()

	temp := t.TempDir()
	configPath := filepath.Join(temp, "config.json")
	output := filepath.Join(temp, "messages.csv")
	cfg := &config.File{DefaultProfile: "test", Profiles: map[string]config.Profile{
		"test": {Server: server.URL, Token: "not-printed"},
	}}
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--config", configPath,
		"messages", "get", "123456789012345",
		"--from", "2026-07-19T11:00:00+05:00",
		"--to", "2026-07-19T12:00:00+05:00",
		"--batch-size", "2",
		"--output", output,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run error: %v\nstderr: %s", err, stderr.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte("not-printed")) || bytes.Contains(stderr.Bytes(), []byte("not-printed")) {
		t.Fatal("token leaked to command output")
	}
	for _, expected := range []string{
		"Unit: Test unit (id=1001, unique_id=123456789012345)",
		"Interval: 2026-07-19T11:00:00+05:00 — 2026-07-19T12:00:00+05:00",
	} {
		if !strings.Contains(stderr.String(), expected) {
			t.Errorf("stderr missing %q:\n%s", expected, stderr.String())
		}
	}
	if strings.Contains(stderr.String(), "Repeat command:") || strings.Contains(stderr.String(), "wln messages get") {
		t.Fatalf("stderr contains a synthesized command:\n%s", stderr.String())
	}
	f, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(f).ReadAll()
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("CSV rows = %d, want header + 3", len(rows))
	}
	if rows[3][column(rows[0], "p.sensor_value")] != "102" {
		t.Fatalf("last row = %v", rows[3])
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"token/login", "core/search_item", "core/search_items", "messages/load_interval", "messages/get_messages", "messages/unload", "core/logout"}
	if len(services) != len(want) {
		t.Fatalf("services = %v, want %v", services, want)
	}
	for i := range want {
		if services[i] != want[i] {
			t.Fatalf("services = %v, want %v", services, want)
		}
	}
}

func TestMessageIntervalDefaultsToStartOfTodayAndNow(t *testing.T) {
	location := time.FixedZone("YEKT", 5*60*60)
	now := time.Date(2026, 8, 3, 14, 27, 31, 123, location)
	from, to, err := messageInterval("", "", now)
	if err != nil {
		t.Fatal(err)
	}
	wantFrom := time.Date(2026, 8, 3, 0, 0, 0, 0, location)
	if !from.Equal(wantFrom) {
		t.Fatalf("from = %s, want %s", from, wantFrom)
	}
	if !to.Equal(now) {
		t.Fatalf("to = %s, want %s", to, now)
	}
}

func TestMessageIntervalUsesExplicitToDateForDefaultStart(t *testing.T) {
	now := time.Date(2026, 8, 3, 14, 0, 0, 0, time.UTC)
	from, to, err := messageInterval("", "2026-07-19T12:00:00+05:00", now)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := from.Format(time.RFC3339), "2026-07-19T00:00:00+05:00"; got != want {
		t.Fatalf("from = %s, want %s", got, want)
	}
	if got, want := to.Format(time.RFC3339), "2026-07-19T12:00:00+05:00"; got != want {
		t.Fatalf("to = %s, want %s", got, want)
	}
}

func TestDefaultMessageOutput(t *testing.T) {
	unit := wialon.Unit{ID: 42, UniqueID: "123456789012345"}
	from := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	if got, want := defaultMessageOutput(unit, from), "wialon-123456789012345-2026-08-03.csv"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func messageJSON(index, sequence int) string {
	return `{"t":` + strconv.Itoa(1000+index) + `,"r":` + strconv.Itoa(999+index) + `,"rt":` + strconv.Itoa(1002+index) + `,"tp":"ud","p":{"sensor_value":` + strconv.Itoa(sequence) + `,"sample_ms":250}}`
}

func column(headers []string, name string) int {
	for i, header := range headers {
		if header == name {
			return i
		}
	}
	return -1
}

func TestProfileListDoesNotPrintToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &config.File{DefaultProfile: "prod", Profiles: map[string]config.Profile{
		"prod": {Server: "https://example.test", Token: "super-secret"},
	}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"--config", path, "profile", "list"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out.Bytes(), []byte("super-secret")) {
		t.Fatal("profile list leaked token")
	}
}

func TestUnitsListShowsHardwareNameInBoxedTable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.Form.Get("svc") {
		case "token/login":
			_, _ = w.Write([]byte(`{"eid":"session"}`))
		case "core/search_items":
			_, _ = w.Write([]byte(`{"items":[{"id":1001,"nm":"Truck 01","uid":"123456789012345","hw":42}]}`))
		case "core/get_hw_types":
			_, _ = w.Write([]byte(`[{"id":42,"name":"Tracker X","hw_category":"tracker"}]`))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", r.Form.Get("svc"))
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &config.File{DefaultProfile: "test", Profiles: map[string]config.Profile{
		"test": {Server: server.URL, Token: "secret"},
	}}
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"--config", configPath, "units", "list"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run error: %v\nstderr: %s", err, stderr.String())
	}
	text := stdout.String()
	for _, expected := range []string{"┌", "┼", "┘", "HARDWARE", "Tracker X"} {
		if !strings.Contains(text, expected) {
			t.Errorf("output missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "42") {
		t.Errorf("hardware ID should not be shown when its name is known:\n%s", text)
	}
}

func TestSanitizeJSONRedactsCredentialsRecursively(t *testing.T) {
	raw, err := sanitizeJSON(json.RawMessage(`{"eid":"session","nested":{"psw":"secret","value":7},"rows":[{"token":"abc"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, secret := range []string{"session", "secret", "abc"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("secret %q leaked in %s", secret, text)
		}
	}
	if !bytes.Contains(raw, []byte(`"value":7`)) {
		t.Fatalf("non-secret value missing from %s", text)
	}
}

func TestProfileLoginWithPasswordSkipsBrowser(t *testing.T) {
	token := strings.Repeat("p", 72)
	var apiURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/login.html", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<script>o.api_url="%s";</script>`, apiURL)
	})
	mux.HandleFunc("/oauth/authorize.html", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.PostForm.Get("login") != "operator" || r.PostForm.Get("passw") != "s3cret" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `<div class='error'>Invalid user name or password</div>`)
			return
		}
		target := fmt.Sprintf("%s?access_token=%s&user_name=operator&state=%s&wialon_sdk_url=%s",
			r.PostForm.Get("redirect_uri"), token, r.URL.Query().Get("state"), apiURL)
		w.Header().Set("Location", target)
		w.WriteHeader(http.StatusFound)
	})
	mux.HandleFunc("/wialon/ajax.html", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("svc") {
		case "token/login":
			_, _ = w.Write([]byte(`{"eid":"session"}`))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", r.Form.Get("svc"))
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	apiURL = server.URL

	previousOpener := openBrowser
	openBrowser = func(string) error {
		t.Error("password login opened a browser")
		return nil
	}
	defer func() { openBrowser = previousOpener }()

	configPath := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("WLN_PASSWORD", "s3cret")
	var stdout, stderr bytes.Buffer
	args := []string{"--config", configPath, "profile", "login", "hosting", "--server", server.URL, "--user", "operator", "--allow-http"}
	if err := Run(context.Background(), args, &stdout, &stderr); err != nil {
		t.Fatalf("Run error: %v\nstderr: %s", err, stderr.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte("s3cret")) || bytes.Contains(stderr.Bytes(), []byte("s3cret")) {
		t.Fatal("password leaked to command output")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if profile := cfg.Profiles["hosting"]; profile.Token != token || profile.Server != server.URL {
		t.Fatalf("saved profile = %#v", profile)
	}

	t.Setenv("WLN_PASSWORD", "wrong")
	err = Run(context.Background(), args, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "Invalid user name or password") {
		t.Fatalf("err = %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	err = Run(context.Background(), []string{"--config", configPath, "profile", "login", "hosting", "--server", server.URL, "--allow-http"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--user is required") {
		t.Fatalf("missing user error = %v", err)
	}
}

func TestProfileReloginReusesSavedLoginSettings(t *testing.T) {
	token := strings.Repeat("c", 72)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		switch r.Form.Get("svc") {
		case "token/login":
			_, _ = w.Write([]byte(`{"eid":"validated-session"}`))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", r.Form.Get("svc"))
		}
	}))
	defer apiServer.Close()

	var loginURL *url.URL
	previousOpener := openBrowser
	openBrowser = func(target string) error {
		login, err := url.Parse(target)
		if err != nil {
			return err
		}
		loginURL = login
		callback, err := url.Parse(login.Query().Get("redirect_uri"))
		if err != nil {
			return err
		}
		query := callback.Query()
		query.Set("state", login.Query().Get("state"))
		query.Set("access_token", token)
		query.Set("wialon_sdk_url", apiServer.URL)
		query.Set("svc_error", "0")
		callback.RawQuery = query.Encode()
		response, err := http.Get(callback.String())
		if response != nil {
			response.Body.Close()
		}
		return err
	}
	defer func() { openBrowser = previousOpener }()

	configPath := filepath.Join(t.TempDir(), "config.json")
	legacy := &config.File{DefaultProfile: "editor", Profiles: map[string]config.Profile{
		"editor": {Server: "https://hst-api.wialon.com", Token: strings.Repeat("a", 72), OperateAs: "sub", Access: 4864},
	}}
	if err := legacy.Save(configPath); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--config", configPath,
		"profile", "login", "editor",
		"--allow-http",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run error: %v\nstderr: %s", err, stderr.String())
	}
	if loginURL.Host != "hosting.wialon.com" || loginURL.Path != "/login.html" {
		t.Fatalf("login URL = %s, want hosting.wialon.com/login.html", loginURL)
	}
	if got := loginURL.Query().Get("access_type"); got != "4864" {
		t.Fatalf("access_type = %s, want saved 4864", got)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	profile := cfg.Profiles["editor"]
	want := config.Profile{Server: apiServer.URL, Token: token, OperateAs: "sub", LoginServer: "https://hosting.wialon.com", Access: 4864}
	if profile != want {
		t.Fatalf("saved profile = %#v, want %#v", profile, want)
	}
}

func TestProfileDeriveIssuesTokenWithoutPassword(t *testing.T) {
	derived := strings.Repeat("d", 72)
	var tokenParams map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("svc") {
		case "token/login":
			_, _ = w.Write([]byte(`{"eid":"session"}`))
		case "token/update":
			_ = json.Unmarshal([]byte(r.Form.Get("params")), &tokenParams)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"h":%q,"fl":1792}`, derived)))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", r.Form.Get("svc"))
		}
	}))
	defer server.Close()

	configPath := filepath.Join(t.TempDir(), "config.json")
	cfg := &config.File{DefaultProfile: "parent", Profiles: map[string]config.Profile{
		"parent": {Server: server.URL, Token: strings.Repeat("a", 72), LoginServer: "https://hosting.wialon.com", Access: 5888},
	}}
	if err := cfg.Save(configPath); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--config", configPath, "profile", "derive", "agent",
		"--access", "1792", "--items", "1001, 1002", "--duration", "720h",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte(derived)) {
		t.Fatal("derived token leaked to command output")
	}
	if tokenParams["callMode"] != "create" || tokenParams["fl"] != float64(1792) || tokenParams["dur"] != float64(2592000) {
		t.Fatalf("token params = %#v", tokenParams)
	}
	if items, ok := tokenParams["items"].([]any); !ok || len(items) != 2 || items[0] != float64(1001) {
		t.Fatalf("items = %#v", tokenParams["items"])
	}
	saved, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := config.Profile{Server: server.URL, Token: derived, LoginServer: "https://hosting.wialon.com", Access: 1792}
	if saved.Profiles["agent"] != want {
		t.Fatalf("derived profile = %#v, want %#v", saved.Profiles["agent"], want)
	}
	if saved.DefaultProfile != "parent" {
		t.Fatalf("default profile = %q", saved.DefaultProfile)
	}

	stdout.Reset()
	if err := Run(context.Background(), []string{"--config", configPath, "profile", "derive", "bad", "--items", "1001,x"}, &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "invalid item ID") {
		t.Fatalf("items error = %v", err)
	}
}

func TestProfilePasswordPrefersFileOverEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte("from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WLN_PASSWORD", "from-env")
	password, err := profilePassword(false, path)
	if err != nil || password != "from-file" {
		t.Fatalf("password = %q, err = %v", password, err)
	}
	if password, err := profilePassword(false, ""); err != nil || password != "from-env" {
		t.Fatalf("environment password = %q, err = %v", password, err)
	}
	if _, err := profilePassword(false, filepath.Join(t.TempDir(), "missing")); err == nil || !strings.Contains(err.Error(), "read password file") {
		t.Fatalf("missing file error = %v", err)
	}
}

func TestProfileLoginDefaultsToWialonHosting(t *testing.T) {
	var opened string
	previousOpener := openBrowser
	openBrowser = func(target string) error {
		opened = target
		return errors.New("browser disabled in tests")
	}
	defer func() { openBrowser = previousOpener }()

	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--config", filepath.Join(t.TempDir(), "config.json"),
		"profile", "login", "fresh", "--callback-timeout", "50ms",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("err = %v", err)
	}
	login, parseErr := url.Parse(opened)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if login.Host != "hosting.wialon.com" || login.Path != "/login.html" {
		t.Fatalf("login URL = %s, want hosting.wialon.com/login.html", opened)
	}
}

func TestProfileLoginBrowserCallbackValidatesAndSaves(t *testing.T) {
	token := strings.Repeat("b", 72)
	var services []string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		services = append(services, r.Form.Get("svc"))
		switch r.Form.Get("svc") {
		case "token/login":
			var params map[string]any
			if err := json.Unmarshal([]byte(r.Form.Get("params")), &params); err != nil {
				t.Fatal(err)
			}
			if params["token"] != token {
				t.Error("issued token was not validated")
			}
			_, _ = w.Write([]byte(`{"eid":"validated-session"}`))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", r.Form.Get("svc"))
		}
	}))
	defer apiServer.Close()

	previousOpener := openBrowser
	openBrowser = func(loginURL string) error {
		login, err := url.Parse(loginURL)
		if err != nil {
			return err
		}
		callback, err := url.Parse(login.Query().Get("redirect_uri"))
		if err != nil {
			return err
		}
		query := callback.Query()
		query.Set("state", login.Query().Get("state"))
		query.Set("access_token", token)
		query.Set("user_name", "operator")
		query.Set("wialon_sdk_url", apiServer.URL)
		query.Set("svc_error", "0")
		callback.RawQuery = query.Encode()
		response, err := http.Get(callback.String())
		if response != nil {
			response.Body.Close()
		}
		return err
	}
	defer func() { openBrowser = previousOpener }()

	configPath := filepath.Join(t.TempDir(), "config.json")
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{
		"--config", configPath,
		"profile", "login", "hosting",
		"--server", "https://hosting.wialon.com",
		"--allow-http",
		"--default",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run error: %v\nstderr: %s", err, stderr.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte(token)) || bytes.Contains(stderr.Bytes(), []byte(token)) {
		t.Fatal("issued token leaked to command output")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	profile := cfg.Profiles["hosting"]
	if cfg.DefaultProfile != "hosting" || profile.Server != apiServer.URL || profile.Token != token {
		t.Fatalf("saved config = %#v", cfg)
	}
	wantServices := []string{"token/login", "core/logout"}
	if !slices.Equal(services, wantServices) {
		t.Fatalf("services = %v, want %v", services, wantServices)
	}
}

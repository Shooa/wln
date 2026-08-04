package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
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
	want := []string{"token/login", "core/search_items", "messages/load_interval", "messages/get_messages", "messages/unload", "core/logout"}
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

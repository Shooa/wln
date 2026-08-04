package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Shooa/wln/internal/config"
)

func testProfile(t *testing.T, server string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &config.File{DefaultProfile: "test", Profiles: map[string]config.Profile{"test": {Server: server, Token: "secret"}}}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUnitsStatusSortsNeverAndStalePointsFirst(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("svc") {
		case "token/login":
			_, _ = w.Write([]byte(`{"eid":"session"}`))
		case "core/search_items":
			_, _ = w.Write([]byte(`{"items":[
              {"id":1,"nm":"Recent","uid":"111","netconn":1,"pos":{"t":4102444800,"y":1,"x":2},"lmsg":{"t":4102444800}},
              {"id":2,"nm":"Never","uid":"222","netconn":0,"lmsg":{"t":100}},
              {"id":3,"nm":"Stale","uid":"333","netconn":0,"pos":{"t":100,"y":3,"x":4},"lmsg":{"t":101}},
              {"id":4,"nm":"Active without position","uid":"444","netconn":0,"lmsg":{"t":4102444800}}
            ]}`))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", r.Form.Get("svc"))
		}
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{"--config", testProfile(t, server.URL), "units", "status", "--offline", "--stale", "24h", "--inactive", "24h"}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, stderr.String())
	}
	text := stdout.String()
	if strings.Contains(text, "Recent") {
		t.Fatalf("online/recent unit was not filtered:\n%s", text)
	}
	if strings.Contains(text, "Active without position") {
		t.Fatalf("recent message without a point was not filtered:\n%s", text)
	}
	if strings.Index(text, "Never") > strings.Index(text, "Stale") {
		t.Fatalf("never should sort first:\n%s", text)
	}
	for _, value := range []string{"LAST POINT", "POINT AGE", "LAST MESSAGE", "never"} {
		if !strings.Contains(text, value) {
			t.Errorf("missing %q", value)
		}
	}
}

func TestResolveMessageIntervalShortcuts(t *testing.T) {
	loc := time.FixedZone("UTC+5", 5*60*60)
	now := time.Date(2026, 8, 4, 12, 30, 0, 0, loc)
	from, to, err := resolveMessageInterval("", "", 2*time.Hour, false, false, "", now)
	if err != nil || !from.Equal(now.Add(-2*time.Hour)) || !to.Equal(now) {
		t.Fatalf("last: %s %s %v", from, to, err)
	}
	from, to, err = resolveMessageInterval("", "", 0, false, true, "", now)
	if err != nil || from.Format(time.RFC3339) != "2026-08-03T00:00:00+05:00" || to.Format(time.RFC3339) != "2026-08-04T00:00:00+05:00" {
		t.Fatalf("yesterday: %s %s %v", from, to, err)
	}
	from, _, err = resolveMessageInterval("", "", 0, false, false, "08:15", now)
	if err != nil || from.Format(time.RFC3339) != "2026-08-04T08:15:00+05:00" {
		t.Fatalf("since: %s %v", from, err)
	}
	if duration, err := parseFlexibleDuration("30d"); err != nil || duration != 30*24*time.Hour {
		t.Fatalf("30d = %s, %v", duration, err)
	}
}

func TestDoctorAndTail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("svc") {
		case "token/login":
			_, _ = w.Write([]byte(`{"eid":"session","tm":1785850000,"user":{"id":7,"nm":"operator"}}`))
		case "core/search_items":
			_, _ = w.Write([]byte(`{"items":[{"id":1,"nm":"Test unit","uid":"123456789012345","hw":42}]}`))
		case "messages/load_last":
			_, _ = w.Write([]byte(`{"count":1,"messages":[{"t":1785850000,"tp":"ud","p":{"value":7}}]}`))
		case "messages/unload":
			_, _ = w.Write([]byte(`{}`))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", r.Form.Get("svc"))
		}
	}))
	defer server.Close()
	configPath := testProfile(t, server.URL)
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), []string{"--config", configPath, "doctor"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"operator", "1 accessible", "login"} {
		if !strings.Contains(out.String(), value) {
			t.Errorf("doctor missing %q", value)
		}
	}
	out.Reset()
	errOut.Reset()
	if err := Run(context.Background(), []string{"--config", configPath, "messages", "tail", "1", "-n", "1", "--format", "ndjson"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &message); err != nil {
		t.Fatalf("tail JSON: %v: %s", err, out.String())
	}
}

func TestNativeMessageExport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.Form.Get("svc") {
		case "token/login":
			_, _ = w.Write([]byte(`{"eid":"session"}`))
		case "core/search_items":
			_, _ = w.Write([]byte(`{"items":[{"id":1,"nm":"Test unit","uid":"123456789012345"}]}`))
		case "exchange/export_messages":
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write([]byte("native-data"))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", r.Form.Get("svc"))
		}
	}))
	defer server.Close()
	var out, errOut bytes.Buffer
	err := Run(context.Background(), []string{"--config", testProfile(t, server.URL), "messages", "export", "1", "--last", "1h", "--format", "wln", "--output", "-"}, &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "native-data" {
		t.Fatalf("download = %q", out.String())
	}
}

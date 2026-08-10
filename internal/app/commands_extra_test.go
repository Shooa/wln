package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Shooa/wln/internal/config"
	"github.com/Shooa/wln/internal/wialon"
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
	err := Run(context.Background(), []string{"--width", "106", "--config", testProfile(t, server.URL), "units", "status", "--offline", "--stale", "24h", "--inactive", "24h"}, &stdout, &stderr)
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
	for _, value := range []string{"LAST POINT", "POINT AGE", "LAST MESSAGE", "MSG AGE", "never"} {
		if !strings.Contains(text, value) {
			t.Errorf("missing %q", value)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if strings.Contains("┌├└│", string([]rune(line)[0])) && utf8.RuneCountInString(line) > 106 {
			t.Errorf("table line exceeds requested width: %q", line)
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
		case "core/search_item":
			_, _ = w.Write([]byte(`{"item":{"id":1,"nm":"Test unit","uid":"123456789012345","hw":42}}`))
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
		case "core/search_item":
			_, _ = w.Write([]byte(`{"item":{"id":1,"nm":"Test unit","uid":"123456789012345"}}`))
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
	configPath := testProfile(t, server.URL)
	var out, errOut bytes.Buffer
	err := Run(context.Background(), []string{"--config", configPath, "messages", "export", "1", "--last", "1h", "--format", "wln", "--output", "-"}, &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "native-data" {
		t.Fatalf("download = %q", out.String())
	}
	if err := Run(context.Background(), []string{"--config", configPath, "messages", "export", "1", "--format", "txt"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "unsupported native format") {
		t.Fatalf("txt error = %v", err)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got, want := truncateRunes("1234567890", 6), "12345…"; got != want {
		t.Fatalf("truncate = %q, want %q", got, want)
	}
	if got := truncateRunes("коротко", 20); got != "коротко" {
		t.Fatalf("short value changed: %q", got)
	}
	if got := truncateRunes("complete", 0); got != "complete" {
		t.Fatalf("unlimited = %q", got)
	}
}

func TestUnitsConnectivityCommands(t *testing.T) {
	var createParams, updateParams map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		var params map[string]any
		if raw := r.Form.Get("params"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &params)
		}
		switch r.Form.Get("svc") {
		case "token/login":
			_, _ = w.Write([]byte(`{"eid":"session","hw_gw_ip":"193.193.165.166","user":{"id":7,"nm":"operator"}}`))
		case "core/search_items":
			_, _ = w.Write([]byte(`{"items":[{"id":1001,"nm":"Truck 01","uid":"old-imei","hw":42}]}`))
		case "core/search_item":
			_, _ = w.Write([]byte(`{"item":{"id":1001,"nm":"Truck 01","uid":"old-imei","hw":42}}`))
		case "core/get_hw_types":
			_, _ = w.Write([]byte(`[
                  {"id":42,"name":"Tracker X","hw_category":"tracker","tp":"20332","up":"20333"},
                  {"id":43,"name":"Mobile App","hw_category":"mobile","tp":"","up":""}
                ]`))
		case "core/create_unit":
			createParams = params
			_, _ = w.Write([]byte(`{"item":{"id":1002,"nm":"Truck 02","hw":42},"flags":257}`))
		case "unit/update_device_type":
			updateParams = params
			_, _ = w.Write([]byte(fmt.Sprintf(`{"uid":%q,"hw":42}`, params["uniqueId"])))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", r.Form.Get("svc"))
		}
	}))
	defer server.Close()
	configPath := testProfile(t, server.URL)

	t.Run("device types", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := Run(context.Background(), []string{"--config", configPath, "units", "device-types", "--search", "tracker", "--format", "json"}, &out, &errOut)
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, errOut.String())
		}
		var types []map[string]any
		if err := json.Unmarshal(out.Bytes(), &types); err != nil {
			t.Fatal(err)
		}
		if len(types) != 1 || types[0]["name"] != "Tracker X" || types[0]["tcp_port"] != "20332" {
			t.Fatalf("types = %#v", types)
		}
	})

	t.Run("connection", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := Run(context.Background(), []string{"--config", configPath, "units", "connection", "1001", "--format", "json"}, &out, &errOut)
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, errOut.String())
		}
		var connection unitConnection
		if err := json.Unmarshal(out.Bytes(), &connection); err != nil {
			t.Fatal(err)
		}
		if connection.UniqueID != "old-imei" || connection.DeviceType != "Tracker X" || connection.ServerAddress != "193.193.165.166" || connection.TCPPort != "20332" || connection.UDPPort != "20333" {
			t.Fatalf("connection = %#v", connection)
		}
	})

	t.Run("create", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := Run(context.Background(), []string{"--config", configPath, "units", "create", "Truck 02", "--device-type", "Tracker X", "--imei", "new-imei", "--format", "json"}, &out, &errOut)
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, errOut.String())
		}
		if createParams["creatorId"] != float64(7) || createParams["hwTypeId"] != float64(42) || createParams["name"] != "Truck 02" {
			t.Fatalf("create params = %#v", createParams)
		}
		if updateParams["itemId"] != float64(1002) || updateParams["uniqueId"] != "new-imei" {
			t.Fatalf("update params = %#v", updateParams)
		}
		var connection unitConnection
		if err := json.Unmarshal(out.Bytes(), &connection); err != nil {
			t.Fatal(err)
		}
		if connection.UnitID != 1002 || connection.UniqueID != "new-imei" {
			t.Fatalf("connection = %#v", connection)
		}
	})

	t.Run("update preserves device type", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := Run(context.Background(), []string{"--config", configPath, "units", "update", "1001", "--imei", "replacement-imei", "--format", "json"}, &out, &errOut)
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, errOut.String())
		}
		if updateParams["itemId"] != float64(1001) || updateParams["deviceTypeId"] != float64(42) || updateParams["uniqueId"] != "replacement-imei" {
			t.Fatalf("update params = %#v", updateParams)
		}
	})
}

func TestExplainConnectivityAccess(t *testing.T) {
	err := explainConnectivityAccess(&wialon.APIError{Code: 7})
	for _, want := range []string{"--access 4864", "Edit connectivity settings"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestAgentUnitsGetUsesCompactJSONAndSkipsUnneededCalls(t *testing.T) {
	var services []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		service := r.Form.Get("svc")
		services = append(services, service)
		switch service {
		case "token/login":
			_, _ = w.Write([]byte(`{"eid":"session","hw_gw_ip":"193.193.165.166"}`))
		case "core/search_item":
			var params map[string]any
			if err := json.Unmarshal([]byte(r.Form.Get("params")), &params); err != nil {
				t.Fatal(err)
			}
			if params["id"] != float64(1001) || params["flags"] != float64(257) {
				t.Errorf("search_item params = %#v", params)
			}
			_, _ = w.Write([]byte(`{"item":{"id":1001,"nm":"Truck 01","uid":"123456789012345","hw":42}}`))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", service)
		}
	}))
	defer server.Close()

	var out, errOut bytes.Buffer
	err := RunAgent(context.Background(), []string{
		"--config", testProfile(t, server.URL), "units", "get", "1001",
		"--fields", "unit_id,name,unique_id",
	}, &out, &errOut)
	if err != nil {
		t.Fatalf("RunAgent: %v\nstderr: %s", err, errOut.String())
	}
	if got, want := out.String(), `{"name":"Truck 01","unique_id":"123456789012345","unit_id":1001}`+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q", errOut.String())
	}
	wantServices := []string{"token/login", "core/search_item", "core/logout"}
	if fmt.Sprint(services) != fmt.Sprint(wantServices) {
		t.Fatalf("services = %v, want %v", services, wantServices)
	}
}

func TestAgentHelpIsJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := RunAgent(context.Background(), []string{"help", "units", "get"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var help map[string]any
	if err := json.Unmarshal(out.Bytes(), &help); err != nil {
		t.Fatalf("help is not JSON: %v: %s", err, out.String())
	}
	if help["command"] != "units get" || !strings.Contains(help["help"].(string), "--fields") {
		t.Fatalf("help = %#v", help)
	}
}

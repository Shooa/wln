package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
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
	var createParams, updateParams, renameParams map[string]any
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
		case "item/update_name":
			renameParams = params
			_, _ = w.Write([]byte(fmt.Sprintf(`{"nm":%q}`, params["name"])))
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

	t.Run("rename only", func(t *testing.T) {
		updateParams = nil
		var out, errOut bytes.Buffer
		err := Run(context.Background(), []string{"--config", configPath, "units", "update", "1001", "--name", "OSMOS_7x", "--format", "json"}, &out, &errOut)
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, errOut.String())
		}
		if renameParams["itemId"] != float64(1001) || renameParams["name"] != "OSMOS_7x" {
			t.Fatalf("rename params = %#v", renameParams)
		}
		if updateParams != nil {
			t.Fatalf("rename called unit/update_device_type: %#v", updateParams)
		}
		var connection unitConnection
		if err := json.Unmarshal(out.Bytes(), &connection); err != nil {
			t.Fatal(err)
		}
		if connection.Name != "OSMOS_7x" || connection.UniqueID != "old-imei" {
			t.Fatalf("connection = %#v", connection)
		}
	})

	t.Run("rename length", func(t *testing.T) {
		var out, errOut bytes.Buffer
		err := Run(context.Background(), []string{"--config", configPath, "units", "update", "1001", "--name", "abc"}, &out, &errOut)
		if err == nil || !strings.Contains(err.Error(), "between 4 and 50") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestAccessDeniedOffersReauthorizationAndRetries(t *testing.T) {
	newToken := strings.Repeat("n", 72)
	var renames []string
	var apiURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		switch r.URL.Path {
		case "/login.html":
			fmt.Fprintf(w, `<script>o.api_url="%s";</script>`, apiURL)
			return
		case "/oauth/authorize.html":
			if r.PostForm.Get("login") != "operator" || r.PostForm.Get("passw") != "s3cret" {
				fmt.Fprint(w, `<div class='error'>Invalid user name or password</div>`)
				return
			}
			w.Header().Set("Location", fmt.Sprintf("%s?access_token=%s&state=%s&wialon_sdk_url=%s",
				r.PostForm.Get("redirect_uri"), newToken, r.URL.Query().Get("state"), apiURL))
			w.WriteHeader(http.StatusFound)
			return
		}
		switch r.Form.Get("svc") {
		case "token/login":
			var params map[string]any
			_ = json.Unmarshal([]byte(r.Form.Get("params")), &params)
			sid := "old-session"
			if params["token"] == newToken {
				sid = "new-session"
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"eid":%q}`, sid)))
		case "core/search_item":
			_, _ = w.Write([]byte(`{"item":{"id":1001,"nm":"Truck 01","uid":"imei","hw":42}}`))
		case "item/update_name":
			renames = append(renames, r.Form.Get("sid"))
			if r.Form.Get("sid") != "new-session" {
				_, _ = w.Write([]byte(`{"error":7}`))
				return
			}
			_, _ = w.Write([]byte(`{"nm":"OSMOS_7x"}`))
		case "core/get_hw_types":
			_, _ = w.Write([]byte(`[{"id":42,"name":"Tracker X","tp":"20332"}]`))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", r.Form.Get("svc"))
		}
	}))
	defer server.Close()

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
		query.Set("access_token", newToken)
		query.Set("wialon_sdk_url", server.URL)
		query.Set("svc_error", "0")
		callback.RawQuery = query.Encode()
		response, err := http.Get(callback.String())
		if response != nil {
			response.Body.Close()
		}
		return err
	}
	defer func() { openBrowser = previousOpener }()
	answer := true
	var questions []string
	previousConfirm := confirmReauthorization
	confirmReauthorization = func(question string) bool {
		questions = append(questions, question)
		return answer
	}
	defer func() { confirmReauthorization = previousConfirm }()

	apiURL = server.URL
	newConfig := func() string {
		path := filepath.Join(t.TempDir(), "config.json")
		cfg := &config.File{DefaultProfile: "editor", Profiles: map[string]config.Profile{
			"editor": {Server: server.URL, Token: "secret", LoginServer: "https://hosting.wialon.com", Access: 4864},
		}}
		if err := cfg.Save(path); err != nil {
			t.Fatal(err)
		}
		return path
	}

	t.Run("declined", func(t *testing.T) {
		answer, renames = false, nil
		var out, errOut bytes.Buffer
		err := Run(context.Background(), []string{"--config", newConfig(), "units", "update", "1001", "--name", "OSMOS_7x"}, &out, &errOut)
		if err == nil || !strings.Contains(err.Error(), "wln profile login editor --access 5888") {
			t.Fatalf("err = %v", err)
		}
		if len(questions) != 1 || !strings.Contains(questions[0], "0x400") {
			t.Fatalf("questions = %q", questions)
		}
	})

	t.Run("accepted", func(t *testing.T) {
		answer, renames, questions = true, nil, nil
		configPath := newConfig()
		var out, errOut bytes.Buffer
		err := Run(context.Background(), []string{"--config", configPath, "units", "update", "1001", "--name", "OSMOS_7x", "--format", "json"}, &out, &errOut)
		if err != nil {
			t.Fatalf("Run: %v\n%s", err, errOut.String())
		}
		if !slices.Equal(renames, []string{"old-session", "new-session"}) {
			t.Fatalf("renames = %v", renames)
		}
		if loginURL.Host != "hosting.wialon.com" || loginURL.Query().Get("access_type") != "5888" {
			t.Fatalf("login URL = %s", loginURL)
		}
		cfg, err := config.Load(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if profile := cfg.Profiles["editor"]; profile.Token != newToken || profile.Access != 5888 {
			t.Fatalf("saved profile = %#v", profile)
		}
	})

	t.Run("agent mode re-authorizes with WLN_PASSWORD", func(t *testing.T) {
		questions, renames = nil, nil
		configPath := filepath.Join(t.TempDir(), "config.json")
		cfg := &config.File{DefaultProfile: "editor", Profiles: map[string]config.Profile{
			"editor": {Server: server.URL, Token: "secret", LoginServer: server.URL, LoginUser: "operator", Access: 4864},
		}}
		if err := cfg.Save(configPath); err != nil {
			t.Fatal(err)
		}
		t.Setenv("WLN_PASSWORD", "s3cret")
		var out, errOut bytes.Buffer
		if err := RunAgent(context.Background(), []string{"--config", configPath, "units", "update", "1001", "--name", "OSMOS_7x"}, &out, &errOut); err != nil {
			t.Fatalf("Run: %v\n%s", err, errOut.String())
		}
		if len(questions) != 0 {
			t.Fatalf("prompted in agent mode: %q", questions)
		}
		if !slices.Equal(renames, []string{"old-session", "new-session"}) {
			t.Fatalf("renames = %v", renames)
		}
		saved, err := config.Load(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if profile := saved.Profiles["editor"]; profile.Token != newToken || profile.Access != 5888 {
			t.Fatalf("saved profile = %#v", profile)
		}
	})

	t.Run("agent mode prints command without prompting", func(t *testing.T) {
		questions = nil
		var out, errOut bytes.Buffer
		err := RunAgent(context.Background(), []string{"--config", newConfig(), "units", "update", "1001", "--name", "OSMOS_7x"}, &out, &errOut)
		if err == nil || !strings.Contains(err.Error(), "wlna profile login editor --access 5888") || len(questions) != 0 {
			t.Fatalf("err = %v, questions = %q", err, questions)
		}
	})
}

func TestExplainRenameAccess(t *testing.T) {
	err := explainRenameAccess(&wialon.APIError{Code: 7})
	for _, want := range []string{"0x400", "Rename right"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestExplainConnectivityAccess(t *testing.T) {
	err := explainConnectivityAccess(&wialon.APIError{Code: 7})
	for _, want := range []string{"0x1000", "Edit connectivity settings"} {
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

package wialon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestClientLoginUnitsAndLogout(t *testing.T) {
	var services []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
			t.Errorf("content type = %q", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		service := r.Form.Get("svc")
		services = append(services, service)
		switch service {
		case "token/login":
			var params map[string]any
			if err := json.Unmarshal([]byte(r.Form.Get("params")), &params); err != nil {
				t.Fatal(err)
			}
			if params["token"] != "test-token" {
				t.Errorf("token not passed to login")
			}
			_, _ = w.Write([]byte(`{"eid":"session"}`))
		case "core/search_items":
			if r.Form.Get("sid") != "session" {
				t.Errorf("sid = %q", r.Form.Get("sid"))
			}
			_, _ = w.Write([]byte(`{"items":[{"id":42,"nm":"unit","uid":"123","uid2":"456","hw":7}]}`))
		case "core/get_hw_types":
			var params map[string]any
			if err := json.Unmarshal([]byte(r.Form.Get("params")), &params); err != nil {
				t.Fatal(err)
			}
			if params["filterType"] != "id" {
				t.Errorf("hardware filter = %#v", params)
			}
			_, _ = w.Write([]byte(`[{"id":7,"name":"Tracker X","hw_category":"tracker"}]`))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", service)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.Login(ctx, "test-token", ""); err != nil {
		t.Fatal(err)
	}
	units, err := client.Units(ctx, "*")
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 || units[0].ID != 42 || units[0].UniqueID != "123" {
		t.Fatalf("units = %#v", units)
	}
	hardware, err := client.HardwareTypes(ctx, []int64{7, 7})
	if err != nil {
		t.Fatal(err)
	}
	if hardware[7].Name != "Tracker X" || hardware[7].Category != "tracker" {
		t.Fatalf("hardware = %#v", hardware)
	}
	if err := client.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := len(services), 4; got != want {
		t.Fatalf("services = %v", services)
	}
}

func TestAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("svc") == "token/login" {
			_, _ = w.Write([]byte(`{"error":7}`))
		}
	}))
	defer server.Close()
	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = client.Login(context.Background(), "bad", "")
	apiErr, ok := errChainAPI(err)
	if !ok || apiErr.Code != 7 {
		t.Fatalf("error = %v, want API error 7", err)
	}
}

func TestUnitConnectivityManagement(t *testing.T) {
	var services []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		service := r.Form.Get("svc")
		services = append(services, service)
		var params map[string]any
		if raw := r.Form.Get("params"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &params); err != nil {
				t.Fatal(err)
			}
		}
		switch service {
		case "token/login":
			_, _ = w.Write([]byte(`{"eid":"session","hw_gw_ip":"193.193.165.166","user":{"id":7,"nm":"operator"}}`))
		case "core/get_hw_types":
			if params["includeType"] != true {
				t.Errorf("get_hw_types params = %#v", params)
			}
			_, _ = w.Write([]byte(`[{"id":42,"name":"Tracker X","hw_category":"tracker","tp":"20332","up":"0","uid2":1}]`))
		case "core/create_unit":
			if params["creatorId"] != float64(7) || params["name"] != "Truck 02" || params["hwTypeId"] != float64(42) || params["dataFlags"] != float64(257) {
				t.Errorf("create params = %#v", params)
			}
			_, _ = w.Write([]byte(`{"item":{"id":1002,"nm":"Truck 02","hw":42},"flags":257}`))
		case "unit/update_device_type":
			if params["itemId"] != float64(1002) || params["deviceTypeId"] != float64(42) || params["uniqueId"] != "123456789012345" {
				t.Errorf("update params = %#v", params)
			}
			_, _ = w.Write([]byte(`{"uid":"123456789012345","hw":42}`))
		case "core/logout":
			_, _ = w.Write([]byte(`{"error":0}`))
		default:
			t.Errorf("unexpected service %q", service)
		}
	}))
	defer server.Close()

	client, err := New(server.URL, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := client.Login(ctx, "token", ""); err != nil {
		t.Fatal(err)
	}
	if got := client.SessionInfo().HardwareGatewayIP; got != "193.193.165.166" {
		t.Fatalf("hardware gateway = %q", got)
	}
	types, err := client.DeviceTypes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 1 || types[0].TCPPort != "20332" || types[0].UDPPort != "" || !types[0].SecondUniqueID {
		t.Fatalf("device types = %#v", types)
	}
	unit, err := client.CreateUnit(ctx, 7, "Truck 02", 42)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := client.UpdateDeviceType(ctx, unit.ID, 42, "123456789012345")
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != 1002 || updated.UniqueID != "123456789012345" || updated.HardwareID != 42 {
		t.Fatalf("updated unit = %#v", updated)
	}
	if err := client.Logout(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"token/login", "core/get_hw_types", "core/create_unit", "unit/update_device_type", "core/logout"}
	if len(services) != len(want) {
		t.Fatalf("services = %v", services)
	}
	for i := range want {
		if services[i] != want[i] {
			t.Fatalf("services = %v, want %v", services, want)
		}
	}
}

func errChainAPI(err error) (*APIError, bool) {
	for err != nil {
		if apiErr, ok := err.(*APIError); ok {
			return apiErr, true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return nil, false
}

func TestRequestIsFormEncoded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		values, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		if values.Get("svc") != "token/login" || values.Get("params") == "" {
			t.Fatalf("form = %v", values)
		}
		_, _ = w.Write([]byte(`{"eid":"session"}`))
	}))
	defer server.Close()
	client, _ := New(server.URL, time.Second)
	if err := client.Login(context.Background(), "token", ""); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPErrorDoesNotReflectToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("reflected-secret-token"))
	}))
	defer server.Close()
	client, _ := New(server.URL, time.Second)
	err := client.Login(context.Background(), "reflected-secret-token", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got == "" || contains(got, "reflected-secret-token") {
		t.Fatalf("unsafe error: %q", got)
	}
}

func contains(text, needle string) bool {
	for i := 0; i+len(needle) <= len(text); i++ {
		if text[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

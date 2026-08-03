package oauthflow

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBuildLoginURL(t *testing.T) {
	base, err := normalizeBaseURL("https://hosting.wialon.com/login.html?old=value")
	if err != nil {
		t.Fatal(err)
	}
	login, err := buildLoginURL(base, "http://127.0.0.1:12345/callback", "state-value", Options{
		ClientID: "wln", Access: 768, Duration: 24 * time.Hour, Language: "ru", User: "tester",
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(login)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "hosting.wialon.com" || u.Path != "/login.html" {
		t.Fatalf("login URL = %s", login)
	}
	want := map[string]string{
		"client_id": "wln", "access_type": "768", "activation_time": "0",
		"duration": "86400", "flags": "5", "redirect_uri": "http://127.0.0.1:12345/callback",
		"response_type": "token", "state": "state-value", "lang": "ru", "user": "tester",
	}
	for name, value := range want {
		if got := u.Query().Get(name); got != value {
			t.Errorf("query %s = %q, want %q", name, got, value)
		}
	}
}

func TestAuthorizeReceivesCallback(t *testing.T) {
	token := strings.Repeat("a", 72)
	var callbackBody string
	opener := func(loginURL string) error {
		u, err := url.Parse(loginURL)
		if err != nil {
			return err
		}
		callback, err := url.Parse(u.Query().Get("redirect_uri"))
		if err != nil {
			return err
		}
		query := callback.Query()
		query.Set("state", u.Query().Get("state"))
		query.Set("access_token", token)
		query.Set("user_name", "tester")
		query.Set("wialon_sdk_url", "https://hst-api.wialon.com")
		query.Set("svc_error", "0")
		callback.RawQuery = query.Encode()
		response, err := http.Get(callback.String())
		if err != nil {
			return err
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		callbackBody = string(body)
		return err
	}

	result, err := Authorize(context.Background(), Options{
		BaseURL: "https://hosting.wialon.com", Access: DefaultAccess,
		CallbackLimit: time.Second,
	}, opener)
	if err != nil {
		t.Fatal(err)
	}
	if result.Token != token || result.UserName != "tester" || result.SDKURL != "https://hst-api.wialon.com" {
		t.Fatalf("result = %#v", result)
	}
	if strings.Contains(callbackBody, token) {
		t.Fatal("callback page leaked token")
	}
	if !strings.Contains(callbackBody, "history.replaceState") {
		t.Fatal("callback page does not clear token-bearing URL")
	}
}

func TestAuthorizeRejectsWrongState(t *testing.T) {
	opener := func(loginURL string) error {
		u, _ := url.Parse(loginURL)
		callback, _ := url.Parse(u.Query().Get("redirect_uri"))
		query := callback.Query()
		query.Set("state", "wrong")
		query.Set("access_token", strings.Repeat("a", 72))
		callback.RawQuery = query.Encode()
		response, err := http.Get(callback.String())
		if response != nil {
			response.Body.Close()
		}
		return err
	}
	_, err := Authorize(context.Background(), Options{
		BaseURL: "https://hosting.wialon.com", CallbackLimit: time.Second,
	}, opener)
	if err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("error = %v, want state mismatch", err)
	}
}

func TestNormalizeBaseURLPreservesLocalPath(t *testing.T) {
	u, err := normalizeBaseURL("https://local.example.test/monitoring/")
	if err != nil {
		t.Fatal(err)
	}
	if got := u.String(); got != "https://local.example.test/monitoring" {
		t.Fatalf("base URL = %q", got)
	}
}

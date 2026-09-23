package oauthflow

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const validToken = "0123456789012345678901234567890123456789012345678901234567890123456789ab"

func wialonStub(t *testing.T, authorize http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/login.html", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `<script>o.api_url="%s";</script>`, strings.ReplaceAll(server.URL, "/", `\/`))
	})
	mux.HandleFunc("/oauth/authorize.html", authorize)
	server = httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestAuthorizePasswordReturnsIssuedToken(t *testing.T) {
	var form url.Values
	var redirectState, apiURL string
	server := wialonStub(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		form = r.PostForm
		redirectState = r.URL.Query().Get("state")
		target := fmt.Sprintf("%s?access_token=%s&user_name=operator&state=%s&svc_error=0&wialon_sdk_url=%s",
			r.PostForm.Get("redirect_uri"), validToken, redirectState, apiURL)
		w.Header().Set("Location", target)
		w.WriteHeader(http.StatusFound)
	})
	apiURL = server.URL

	result, err := AuthorizePassword(context.Background(), Options{
		BaseURL: server.URL, Access: 1792, Duration: 24 * time.Hour, Language: "ru",
	}, " operator ", "secret")
	if err != nil {
		t.Fatalf("AuthorizePassword: %v", err)
	}
	if result.Token != validToken || result.UserName != "operator" || result.SDKURL != server.URL {
		t.Fatalf("result = %#v", result)
	}
	if form.Get("login") != "operator" || form.Get("passw") != "secret" {
		t.Fatalf("credentials = %#v", form)
	}
	for field, want := range map[string]string{
		"response_type": "token", "client_id": "wln", "access_type": "1792",
		"activation_time": "0", "duration": "86400", "flags": "5",
		"wialon_sdk_url": server.URL, "login_uri": server.URL + "/login.html",
	} {
		if got := form.Get(field); got != want {
			t.Errorf("form[%s] = %q, want %q", field, got, want)
		}
	}
	if redirectState == "" || form.Get("redirect_uri") == "" {
		t.Fatalf("state = %q, redirect_uri = %q", redirectState, form.Get("redirect_uri"))
	}
}

func TestAuthorizePasswordReportsLoginPageFailures(t *testing.T) {
	tests := []struct {
		name string
		page string
		want string
	}{
		{name: "rejected", page: `<div class='error'>Invalid user name or password</div>`, want: "Invalid user name or password"},
		{name: "two factor", page: `<input type="hidden" name="auth_type" value="1">`, want: "two-factor"},
		{name: "unexpected", page: `<html></html>`, want: "no token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := wialonStub(t, func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, tt.page)
			})
			_, err := AuthorizePassword(context.Background(), Options{BaseURL: server.URL}, "operator", "secret")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestAuthorizePasswordRejectsMismatchedStateAndShortToken(t *testing.T) {
	stateServer := wialonStub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Location", r.PostForm.Get("redirect_uri")+"?access_token="+validToken+"&state=forged")
		w.WriteHeader(http.StatusFound)
	})
	if _, err := AuthorizePassword(context.Background(), Options{BaseURL: stateServer.URL}, "operator", "secret"); err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("err = %v", err)
	}

	shortServer := wialonStub(t, func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Location", r.PostForm.Get("redirect_uri")+"?access_token=short")
		w.WriteHeader(http.StatusFound)
	})
	if _, err := AuthorizePassword(context.Background(), Options{BaseURL: shortServer.URL}, "operator", "secret"); err == nil || !strings.Contains(err.Error(), "invalid length") {
		t.Fatalf("err = %v", err)
	}
}

func TestAuthorizePasswordRequiresCredentials(t *testing.T) {
	if _, err := AuthorizePassword(context.Background(), Options{BaseURL: "https://hosting.wialon.com"}, " ", "secret"); err == nil || !strings.Contains(err.Error(), "user name is required") {
		t.Fatalf("err = %v", err)
	}
	if _, err := AuthorizePassword(context.Background(), Options{BaseURL: "https://hosting.wialon.com"}, "operator", ""); err == nil || !strings.Contains(err.Error(), "password is required") {
		t.Fatalf("err = %v", err)
	}
}

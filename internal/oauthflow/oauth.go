package oauthflow

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAccess        = 0x100 + 0x200
	defaultClientID      = "wln"
	defaultCallbackLimit = 5 * time.Minute
)

type Options struct {
	BaseURL        string
	ClientID       string
	Access         int64
	Duration       time.Duration
	Language       string
	User           string
	CallbackLimit  time.Duration
	DisableBrowser bool
}

type Result struct {
	Token    string
	UserName string
	SDKURL   string
	LoginURL string
	Callback string
}

type Opener func(string) error

type callbackResult struct {
	token    string
	userName string
	sdkURL   string
	err      error
}

func Authorize(ctx context.Context, options Options, open Opener) (Result, error) {
	base, err := normalizeBaseURL(options.BaseURL)
	if err != nil {
		return Result{}, err
	}
	if options.ClientID == "" {
		options.ClientID = defaultClientID
	}
	if options.Access == 0 {
		options.Access = DefaultAccess
	}
	if options.Duration < 0 {
		return Result{}, errors.New("token duration must not be negative")
	}
	if options.CallbackLimit <= 0 {
		options.CallbackLimit = defaultCallbackLimit
	}
	state, err := randomState()
	if err != nil {
		return Result{}, err
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return Result{}, fmt.Errorf("listen for Wialon callback: %w", err)
	}
	callbackURL := "http://" + listener.Addr().String() + "/callback"
	loginURL, err := buildLoginURL(base, callbackURL, state, options)
	if err != nil {
		listener.Close()
		return Result{}, err
	}

	resultCh := make(chan callbackResult, 1)
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler:           callbackHandler(state, base, resultCh),
	}
	serveErr := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if !options.DisableBrowser {
		if open == nil {
			return Result{}, errors.New("no browser opener configured")
		}
		if err := open(loginURL); err != nil {
			return Result{}, fmt.Errorf("open Wialon login page: %w", err)
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, options.CallbackLimit)
	defer cancel()
	select {
	case got := <-resultCh:
		if got.err != nil {
			return Result{}, got.err
		}
		return Result{
			Token: got.token, UserName: got.userName, SDKURL: got.sdkURL,
			LoginURL: loginURL, Callback: callbackURL,
		}, nil
	case err := <-serveErr:
		return Result{}, fmt.Errorf("serve Wialon callback: %w", err)
	case <-waitCtx.Done():
		if errors.Is(waitCtx.Err(), context.DeadlineExceeded) {
			return Result{}, fmt.Errorf("Wialon login callback timed out after %s", options.CallbackLimit)
		}
		return Result{}, waitCtx.Err()
	}
}

func normalizeBaseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("Wialon base URL is required")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("invalid Wialon base URL %q", raw)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, fmt.Errorf("Wialon base URL must use HTTP or HTTPS, got %q", u.Scheme)
	}
	if u.User != nil {
		return nil, errors.New("Wialon base URL must not contain credentials")
	}
	u.RawQuery = ""
	u.Fragment = ""
	path := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/wialon/ajax.html", "/login.html"} {
		if strings.HasSuffix(path, suffix) {
			path = strings.TrimSuffix(path, suffix)
		}
	}
	u.Path = path
	return u, nil
}

func buildLoginURL(base *url.URL, callbackURL, state string, options Options) (string, error) {
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + "/login.html"
	query := u.Query()
	query.Set("client_id", options.ClientID)
	query.Set("access_type", strconv.FormatInt(options.Access, 10))
	query.Set("activation_time", "0")
	query.Set("duration", strconv.FormatInt(int64(options.Duration/time.Second), 10))
	query.Set("flags", "5") // 0x1: user_name; 0x4: custom state parameter.
	query.Set("redirect_uri", callbackURL)
	query.Set("response_type", "token")
	query.Set("state", state)
	if options.Language != "" {
		query.Set("lang", options.Language)
	}
	if options.User != "" {
		query.Set("user", options.User)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func callbackHandler(expectedState string, base *url.URL, resultCh chan<- callbackResult) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		setBrowserSafetyHeaders(w)
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query := r.URL.Query()
		if !secureEqual(query.Get("state"), expectedState) {
			sendResult(resultCh, callbackResult{err: errors.New("Wialon callback state mismatch")})
			http.Error(w, "Authorization callback rejected: invalid state.", http.StatusBadRequest)
			return
		}
		if code := query.Get("svc_error"); code != "" && code != "0" {
			sendResult(resultCh, callbackResult{err: fmt.Errorf("Wialon authorization failed with svc_error=%s", code)})
			http.Error(w, "Wialon authorization failed.", http.StatusBadRequest)
			return
		}
		token := query.Get("access_token")
		if token == "" {
			sendResult(resultCh, callbackResult{err: errors.New("Wialon callback contained no access_token")})
			http.Error(w, "Authorization callback did not contain a token.", http.StatusBadRequest)
			return
		}
		if len(token) != 72 {
			sendResult(resultCh, callbackResult{err: fmt.Errorf("Wialon callback returned a token with invalid length %d", len(token))})
			http.Error(w, "Authorization callback contained an invalid token.", http.StatusBadRequest)
			return
		}
		sdkURL := query.Get("wialon_sdk_url")
		if sdkURL == "" {
			sdkURL = base.String()
		}
		validatedSDK, err := normalizeBaseURL(sdkURL)
		if err != nil {
			sendResult(resultCh, callbackResult{err: fmt.Errorf("invalid wialon_sdk_url in callback: %w", err)})
			http.Error(w, "Authorization callback contained an invalid API URL.", http.StatusBadRequest)
			return
		}
		if err := successPage.Execute(w, nil); err != nil {
			sendResult(resultCh, callbackResult{err: fmt.Errorf("render callback page: %w", err)})
			return
		}
		sendResult(resultCh, callbackResult{token: token, userName: query.Get("user_name"), sdkURL: validatedSDK.String()})
	})
	mux.HandleFunc("/done", func(w http.ResponseWriter, _ *http.Request) {
		setBrowserSafetyHeaders(w)
		_ = successPage.Execute(w, nil)
	})
	return mux
}

func sendResult(ch chan<- callbackResult, result callbackResult) {
	select {
	case ch <- result:
	default:
	}
}

func setBrowserSafetyHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
}

func secureEqual(actual, expected string) bool {
	if len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

func randomState() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate callback state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

var successPage = template.Must(template.New("success").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>wln authorization complete</title>
<style>body{font:16px system-ui,sans-serif;max-width:42rem;margin:12vh auto;padding:0 2rem;color:#17202a}h1{font-size:1.5rem}</style>
</head><body><h1>Authorization complete</h1><p>The Wialon profile was received. You can close this tab and return to the terminal.</p>
<script>history.replaceState({}, document.title, '/done');</script></body></html>`))

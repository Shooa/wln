package oauthflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Wialon's login.html posts the account credentials to this form on the Remote
// API host and answers with the same redirect the browser flow receives. The
// page also carries a "sign" field, which the server does not verify.
const authorizePath = "/oauth/authorize.html"

var (
	apiURLPattern     = regexp.MustCompile(`o\.api_url="([^"]+)"`)
	accessTokenInBody = regexp.MustCompile(`access_token=([0-9A-Za-z]{72})`)
	userNameInBody    = regexp.MustCompile(`user_name=([^&"'<>\s]*)`)
	errorInBody       = regexp.MustCompile(`class=['"]error['"][^>]*>([^<]+)`)
)

// AuthorizePassword exchanges Wialon account credentials for a token without a
// browser. It fails when the account requires two-factor authentication.
func AuthorizePassword(ctx context.Context, options Options, user, password string) (Result, error) {
	base, err := normalizeBaseURL(options.BaseURL)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(user) == "" {
		return Result{}, errors.New("Wialon user name is required for a password login")
	}
	if password == "" {
		return Result{}, errors.New("Wialon password is required for a password login")
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
	state, err := randomState()
	if err != nil {
		return Result{}, err
	}
	client := &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	apiURL := discoverAPIURL(ctx, client, base)
	// The redirect target is never contacted: the issued token is read from the
	// redirect itself instead of from a local callback server.
	redirectURI := "http://127.0.0.1/callback"
	action := *apiURL
	action.Path = strings.TrimRight(action.Path, "/") + authorizePath
	query := url.Values{"state": {state}}
	if options.Language != "" {
		query.Set("lang", options.Language)
	}
	action.RawQuery = query.Encode()

	form := url.Values{
		"response_type":   {"token"},
		"wialon_sdk_url":  {apiURL.String()},
		"success_uri":     {redirectURI},
		"login_uri":       {strings.TrimRight(base.String(), "/") + "/login.html"},
		"client_id":       {options.ClientID},
		"redirect_uri":    {redirectURI},
		"access_type":     {strconv.FormatInt(options.Access, 10)},
		"activation_time": {"0"},
		"duration":        {strconv.FormatInt(int64(options.Duration/time.Second), 10)},
		"flags":           {"5"}, // 0x1: user_name; 0x4: custom state parameter.
		"login":           {strings.TrimSpace(user)},
		"passw":           {password},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, action.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return Result{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := client.Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("submit Wialon credentials: %w", err)
	}
	defer response.Body.Close()
	return resultFromAuthorize(response, apiURL, state)
}

// discoverAPIURL reads the Remote API address that login.html reports. Wialon
// Hosting serves the login page and the API from different hosts; Wialon Local
// falls back to the installation itself.
func discoverAPIURL(ctx context.Context, client *http.Client, base *url.URL) *url.URL {
	target := *base
	target.Path = strings.TrimRight(target.Path, "/") + "/login.html"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return base
	}
	response, err := client.Do(request)
	if err != nil {
		return base
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return base
	}
	match := apiURLPattern.FindSubmatch(body)
	if match == nil {
		return base
	}
	discovered, err := normalizeBaseURL(strings.ReplaceAll(string(match[1]), `\/`, "/"))
	if err != nil {
		return base
	}
	return discovered
}

func resultFromAuthorize(response *http.Response, base *url.URL, state string) (Result, error) {
	if location := response.Header.Get("Location"); location != "" {
		redirect, err := url.Parse(location)
		if err != nil {
			return Result{}, fmt.Errorf("parse Wialon authorization redirect: %w", err)
		}
		query := redirect.Query()
		if got := query.Get("state"); got != "" && !secureEqual(got, state) {
			return Result{}, errors.New("Wialon authorization state mismatch")
		}
		if code := query.Get("svc_error"); code != "" && code != "0" {
			return Result{}, fmt.Errorf("Wialon authorization failed with svc_error=%s", code)
		}
		return buildResult(query.Get("access_token"), query.Get("user_name"), query.Get("wialon_sdk_url"), base)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return Result{}, fmt.Errorf("read Wialon authorization response: %w", err)
	}
	page := string(body)
	if match := accessTokenInBody.FindStringSubmatch(page); match != nil {
		userName := ""
		if named := userNameInBody.FindStringSubmatch(page); named != nil {
			userName, _ = url.QueryUnescape(named[1])
		}
		return buildResult(match[1], userName, "", base)
	}
	if strings.Contains(page, "two_factor") || strings.Contains(page, `name="auth_type"`) {
		return Result{}, errors.New("the account requires two-factor authentication; authorize in a browser instead")
	}
	if match := errorInBody.FindStringSubmatch(page); match != nil {
		return Result{}, fmt.Errorf("Wialon rejected the login: %s", strings.TrimSpace(match[1]))
	}
	return Result{}, fmt.Errorf("Wialon returned no token (HTTP %d); authorize in a browser instead", response.StatusCode)
}

func buildResult(token, userName, sdkURL string, base *url.URL) (Result, error) {
	if token == "" {
		return Result{}, errors.New("Wialon authorization returned no access_token")
	}
	if len(token) != 72 {
		return Result{}, fmt.Errorf("Wialon returned a token with invalid length %d", len(token))
	}
	if sdkURL == "" {
		sdkURL = base.String()
	}
	validated, err := normalizeBaseURL(sdkURL)
	if err != nil {
		return Result{}, fmt.Errorf("invalid wialon_sdk_url in Wialon response: %w", err)
	}
	return Result{Token: token, UserName: userName, SDKURL: validated.String()}, nil
}

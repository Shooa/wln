package wialon

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxResponseBytes = 512 << 20

var Version = "0.6.1"

type Client struct {
	endpoint string
	http     *http.Client
	sid      string
	info     SessionInfo
}

type SessionInfo struct {
	UserID     int64  `json:"user_id,omitempty"`
	UserName   string `json:"user_name,omitempty"`
	ServerTime int64  `json:"server_time,omitempty"`
}

type APIError struct {
	Code int
}

func (e *APIError) Error() string {
	if text, ok := errorText[e.Code]; ok {
		return fmt.Sprintf("Wialon API error %d: %s", e.Code, text)
	}
	return fmt.Sprintf("Wialon API error %d", e.Code)
}

var errorText = map[int]string{
	-101: "wrong network response",
	-100: "network timeout",
	1:    "invalid or expired session",
	2:    "invalid API service name",
	3:    "invalid result",
	4:    "invalid input",
	5:    "request failed",
	6:    "unknown or internal error",
	7:    "access denied",
	8:    "invalid username or password",
	9:    "authorization server unavailable",
	10:   "concurrent request limit reached",
	1001: "no messages for the selected interval",
	1003: "request or service limit reached",
	1004: "message limit exceeded",
	1005: "execution time limit exceeded",
	1011: "IP changed or session expired",
}

func New(server string, timeout time.Duration) (*Client, error) {
	endpoint, err := endpointURL(server)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &Client{endpoint: endpoint, http: &http.Client{Timeout: timeout}}, nil
}

func endpointURL(server string) (string, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", errors.New("empty Wialon server URL")
	}
	u, err := url.Parse(server)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid Wialon server URL %q", server)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", fmt.Errorf("unsupported Wialon server scheme %q", u.Scheme)
	}
	path := strings.TrimRight(u.Path, "/")
	if path != "/wialon/ajax.html" {
		path += "/wialon/ajax.html"
	}
	u.Path = path
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

func (c *Client) Login(ctx context.Context, token, operateAs string) error {
	params := map[string]any{"token": token, "fl": 3}
	if operateAs != "" {
		params["operateAs"] = operateAs
	}
	var response struct {
		EID  string `json:"eid"`
		Time int64  `json:"tm"`
		User struct {
			ID   int64  `json:"id"`
			Name string `json:"nm"`
		} `json:"user"`
	}
	if err := c.call(ctx, "token/login", params, "", &response); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if response.EID == "" {
		return errors.New("login: Wialon returned no session ID")
	}
	c.sid = response.EID
	c.info = SessionInfo{UserID: response.User.ID, UserName: response.User.Name, ServerTime: response.Time}
	return nil
}

func (c *Client) SessionInfo() SessionInfo { return c.info }

func (c *Client) Logout(ctx context.Context) error {
	if c.sid == "" {
		return nil
	}
	var response json.RawMessage
	err := c.call(ctx, "core/logout", map[string]any{}, c.sid, &response)
	c.sid = ""
	return err
}

func (c *Client) Call(ctx context.Context, service string, params any, out any) error {
	if c.sid == "" {
		return errors.New("not logged in")
	}
	return c.call(ctx, service, params, c.sid, out)
}

func (c *Client) CallRaw(ctx context.Context, service string, params any) (json.RawMessage, error) {
	var raw json.RawMessage
	if err := c.Call(ctx, service, params, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *Client) call(ctx context.Context, service string, params any, sid string, out any) error {
	encoded, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode parameters: %w", err)
	}
	form := url.Values{"svc": {service}, "params": {string(encoded)}}
	if sid != "" {
		form.Set("sid", sid)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "wln/"+Version)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", service, err)
	}
	defer resp.Body.Close()
	var body io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return fmt.Errorf("decode gzip response: %w", err)
		}
		defer gz.Close()
		body = gz
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Do not include the response body: an intermediary may reflect request
		// fields, including the login token.
		return fmt.Errorf("HTTP %s from Wialon", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(data) > maxResponseBytes {
		return fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	var apiErr struct {
		Error *int `json:"error"`
	}
	if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != nil && *apiErr.Error != 0 {
		return &APIError{Code: *apiErr.Error}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode %s response: %w", service, err)
	}
	return nil
}

type Unit struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	UniqueID   string `json:"unique_id"`
	UniqueID2  string `json:"unique_id2,omitempty"`
	Hardware   string `json:"hardware,omitempty"`
	HardwareID int64  `json:"hardware_id,omitempty"`
}

type Position struct {
	Time       int64   `json:"time,omitempty"`
	Latitude   float64 `json:"latitude,omitempty"`
	Longitude  float64 `json:"longitude,omitempty"`
	Altitude   float64 `json:"altitude,omitempty"`
	Speed      int     `json:"speed,omitempty"`
	Course     int     `json:"course,omitempty"`
	Satellites int     `json:"satellites,omitempty"`
}

type UnitStatus struct {
	ID              int64    `json:"id"`
	Name            string   `json:"name"`
	UniqueID        string   `json:"unique_id,omitempty"`
	Online          bool     `json:"online"`
	LastMessageTime int64    `json:"last_message_time,omitempty"`
	PointAgeSeconds int64    `json:"point_age_seconds,omitempty"`
	Position        Position `json:"position,omitempty"`
}

func (c *Client) Units(ctx context.Context, nameMask string) ([]Unit, error) {
	if nameMask == "" {
		nameMask = "*"
	}
	params := map[string]any{
		"spec": map[string]any{
			"itemsType":     "avl_unit",
			"propName":      "sys_name",
			"propValueMask": nameMask,
			"sortType":      "sys_name",
			"propType":      "property",
		},
		"force": 1,
		"flags": 257,
		"from":  0,
		"to":    0,
	}
	var response struct {
		Items []struct {
			ID   int64  `json:"id"`
			Name string `json:"nm"`
			UID  string `json:"uid"`
			UID2 string `json:"uid2"`
			HW   int64  `json:"hw"`
		} `json:"items"`
	}
	if err := c.Call(ctx, "core/search_items", params, &response); err != nil {
		return nil, fmt.Errorf("list units: %w", err)
	}
	units := make([]Unit, 0, len(response.Items))
	for _, item := range response.Items {
		units = append(units, Unit{ID: item.ID, Name: item.Name, UniqueID: item.UID, UniqueID2: item.UID2, HardwareID: item.HW})
	}
	return units, nil
}

func (c *Client) UnitStatuses(ctx context.Context, nameMask string) ([]UnitStatus, error) {
	if nameMask == "" {
		nameMask = "*"
	}
	params := map[string]any{
		"spec": map[string]any{
			"itemsType": "avl_unit", "propName": "sys_name", "propValueMask": nameMask,
			"sortType": "sys_name", "propType": "property",
		},
		"force": 1, "flags": 1 + 256 + 1024 + 2097152 + 4194304, "from": 0, "to": 0,
	}
	var response struct {
		Items []struct {
			ID       int64           `json:"id"`
			Name     string          `json:"nm"`
			UID      string          `json:"uid"`
			NetConn  json.RawMessage `json:"netconn"`
			Position *struct {
				Time       int64   `json:"t"`
				Latitude   float64 `json:"y"`
				Longitude  float64 `json:"x"`
				Altitude   float64 `json:"z"`
				Speed      int     `json:"s"`
				Course     int     `json:"c"`
				Satellites int     `json:"sc"`
			} `json:"pos"`
			LastMessage *struct {
				Time int64 `json:"t"`
			} `json:"lmsg"`
		} `json:"items"`
	}
	if err := c.Call(ctx, "core/search_items", params, &response); err != nil {
		return nil, fmt.Errorf("get unit status: %w", err)
	}
	statuses := make([]UnitStatus, 0, len(response.Items))
	for _, item := range response.Items {
		status := UnitStatus{ID: item.ID, Name: item.Name, UniqueID: item.UID, Online: connectionValue(item.NetConn)}
		if item.LastMessage != nil {
			status.LastMessageTime = item.LastMessage.Time
		}
		if item.Position != nil {
			status.Position = Position{Time: item.Position.Time, Latitude: item.Position.Latitude, Longitude: item.Position.Longitude,
				Altitude: item.Position.Altitude, Speed: item.Position.Speed, Course: item.Position.Course, Satellites: item.Position.Satellites}
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func connectionValue(raw json.RawMessage) bool {
	var boolean bool
	if json.Unmarshal(raw, &boolean) == nil {
		return boolean
	}
	var number int
	return json.Unmarshal(raw, &number) == nil && number != 0
}

type HardwareType struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category,omitempty"`
}

func (c *Client) HardwareTypes(ctx context.Context, ids []int64) (map[int64]HardwareType, error) {
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]bool)
	for _, id := range ids {
		if id == 0 || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return map[int64]HardwareType{}, nil
	}
	params := map[string]any{
		"filterType":   "id",
		"filterValue":  unique,
		"includeType":  true,
		"ignoreRename": false,
	}
	var response []struct {
		ID       int64  `json:"id"`
		Name     string `json:"name"`
		Category string `json:"hw_category"`
	}
	if err := c.Call(ctx, "core/get_hw_types", params, &response); err != nil {
		return nil, fmt.Errorf("get hardware types: %w", err)
	}
	result := make(map[int64]HardwareType, len(response))
	for _, hardware := range response {
		result[hardware.ID] = HardwareType{ID: hardware.ID, Name: hardware.Name, Category: hardware.Category}
	}
	return result, nil
}

type LoadResult struct {
	Count    int              `json:"count"`
	Messages []map[string]any `json:"messages"`
}

func (c *Client) LoadMessages(ctx context.Context, unitID int64, from, to int64, batchSize int, allTypes bool) (LoadResult, error) {
	flags, mask := 0, 65280
	if allTypes {
		mask = 0
	}
	params := map[string]any{
		"itemId": unitID, "timeFrom": from, "timeTo": to,
		"flags": flags, "flagsMask": mask, "loadCount": batchSize,
	}
	var response LoadResult
	if err := c.Call(ctx, "messages/load_interval", params, &response); err != nil {
		return LoadResult{}, fmt.Errorf("load messages: %w", err)
	}
	return response, nil
}

func (c *Client) LoadLast(ctx context.Context, unitID, lastTime int64, count int, allTypes bool) (LoadResult, error) {
	flags, mask := 0, 65280
	if allTypes {
		mask = 0
	}
	params := map[string]any{
		"itemId": unitID, "lastTime": lastTime, "lastCount": count,
		"flags": flags, "flagsMask": mask, "loadCount": count,
	}
	var response LoadResult
	if err := c.Call(ctx, "messages/load_last", params, &response); err != nil {
		return LoadResult{}, fmt.Errorf("load last messages: %w", err)
	}
	return response, nil
}

func (c *Client) GetMessages(ctx context.Context, from, to int) ([]map[string]any, error) {
	params := map[string]any{"indexFrom": from, "indexTo": to}
	var response []map[string]any
	if err := c.Call(ctx, "messages/get_messages", params, &response); err != nil {
		return nil, fmt.Errorf("get messages %d:%d: %w", from, to, err)
	}
	return response, nil
}

func (c *Client) UnloadMessages(ctx context.Context) error {
	var response json.RawMessage
	if err := c.Call(ctx, "messages/unload", map[string]any{}, &response); err != nil {
		return fmt.Errorf("unload messages: %w", err)
	}
	return nil
}

func (c *Client) Download(ctx context.Context, service string, params any) ([]byte, string, error) {
	if c.sid == "" {
		return nil, "", errors.New("not logged in")
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, "", fmt.Errorf("encode parameters: %w", err)
	}
	form := url.Values{"svc": {service}, "params": {string(encoded)}, "sid": {c.sid}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "wln/"+Version)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("request %s: %w", service, err)
	}
	defer resp.Body.Close()
	var body io.Reader = resp.Body
	if strings.EqualFold(resp.Header.Get("Content-Encoding"), "gzip") {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, "", fmt.Errorf("decode gzip response: %w", err)
		}
		defer gz.Close()
		body = gz
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("HTTP %s from Wialon", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read download: %w", err)
	}
	if len(data) > maxResponseBytes {
		return nil, "", fmt.Errorf("download exceeds %d bytes", maxResponseBytes)
	}
	var apiErr struct {
		Error *int `json:"error"`
	}
	if json.Unmarshal(data, &apiErr) == nil && apiErr.Error != nil && *apiErr.Error != 0 {
		return nil, "", &APIError{Code: *apiErr.Error}
	}
	return data, resp.Header.Get("Content-Type"), nil
}

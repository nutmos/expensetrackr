package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxResponseSize bounds an API response body read by the client.
const maxResponseSize = 16 << 20

// Client calls the expensetrackr HTTP API with a personal API token.
type Client struct {
	// BaseURL is the server root, e.g. http://127.0.0.1:8080 (no /api).
	BaseURL string
	// Token is a personal API token (ext_...). A read token is enough for
	// the read tools; the write tools need a write token.
	Token string
	// HTTP is the client used for requests; nil means a client with a
	// 2-minute timeout.
	HTTP *http.Client
}

// APIError is a non-2xx answer from the API.
type APIError struct {
	Status int
	Body   json.RawMessage // the JSON error body ({"error", "fields", "code", ...})
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("expensetrackr API returned HTTP %d %s", e.Status, http.StatusText(e.Status))
	if len(e.Body) > 0 {
		msg += ": " + string(e.Body)
	}
	if hint := statusHint(e.Status, e.Body); hint != "" {
		msg += "\nHint: " + hint
	}
	return msg
}

// Code returns the API's machine-readable error code, if any.
func (e *APIError) Code() string {
	var b struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(e.Body, &b)
	return b.Code
}

func statusHint(status int, body json.RawMessage) string {
	switch status {
	case http.StatusUnauthorized:
		return "the API token is missing, wrong, expired or revoked; ask the user to check EXPENSETRACKR_TOKEN. Do not retry."
	case http.StatusForbidden:
		if bytes.Contains(body, []byte("insufficient_scope")) {
			return "this is a read-only token; ask the user for a token with the write scope."
		}
	case http.StatusNotFound:
		return "no such record for this user; list again and check the uid."
	case http.StatusConflict:
		if bytes.Contains(body, []byte("version_conflict")) {
			return `the record changed since it was read; "current" is the record now. Re-check it with the user if needed, then retry with its version.`
		}
	case http.StatusUnprocessableEntity:
		return `"fields" names each invalid field; fix those and try again (ask the user for anything you would otherwise have to guess).`
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "the exchange-rate service may be down; retry later or send to_amount (the amount received) from the user."
	}
	return ""
}

// response is a successful API answer.
type response struct {
	Status int
	ETag   string
	Body   []byte
}

// do sends one request. query may be nil; body (if not nil) is sent as JSON;
// ifMatch, if not empty, is sent as If-Match. A non-2xx answer is *APIError.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, ifMatch string) (response, error) {
	u := strings.TrimRight(c.BaseURL, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return response{}, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return response{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "expensetrackr-mcp/"+ServerVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	res, err := hc.Do(req)
	if err != nil {
		return response{}, fmt.Errorf("cannot reach the expensetrackr server at %s: %w", c.BaseURL, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseSize))
	if err != nil {
		return response{}, fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		apiErr := &APIError{Status: res.StatusCode}
		if json.Valid(data) {
			apiErr.Body = data
		} else if len(data) > 0 {
			apiErr.Body, _ = json.Marshal(map[string]string{"error": strings.TrimSpace(string(data))})
		}
		return response{}, apiErr
	}
	return response{Status: res.StatusCode, ETag: res.Header.Get("ETag"), Body: data}, nil
}

// getJSON GETs path and decodes the JSON answer into a generic value.
func (c *Client) getJSON(ctx context.Context, path string, query url.Values) (any, response, error) {
	res, err := c.do(ctx, http.MethodGet, path, query, nil, "")
	if err != nil {
		return nil, res, err
	}
	v, err := decode(res.Body)
	return v, res, err
}

func decode(b []byte) (any, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber() // keep amount_minor etc. exact
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode API response: %w", err)
	}
	return v, nil
}

// etag formats a version as the API's entity tag.
func etag(version int64) string { return strconv.Quote(strconv.FormatInt(version, 10)) }

// errNotObject is returned when an API answer is not the expected object.
var errNotObject = errors.New("unexpected API response (not a JSON object)")

// Copyright 2026 Alibaba Group Holding Ltd.
// SPDX-License-Identifier: Apache-2.0

package opensandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const execdPort = 44772

// ActivitySnapshot is the idle decision input returned by execd.
type ActivitySnapshot struct {
	LastActivityAt   time.Time  `json:"last_activity_at"`
	ObservedAt       time.Time  `json:"observed_at"`
	Busy             bool       `json:"busy"`
	ActiveOperations uint64     `json:"active_operations"`
	Revision         uint64     `json:"revision"`
	KeepAwakeUntil   *time.Time `json:"keep_awake_until,omitempty"`
}

// Endpoint is a lifecycle-resolved sandbox endpoint and required headers.
type Endpoint struct {
	URL     string
	Headers map[string]string
}

// Lifecycle defines the OpenSandbox operations required by the reconciler.
type Lifecycle interface {
	ResolveExecdEndpoint(context.Context, string) (Endpoint, error)
	Activity(context.Context, Endpoint) (ActivitySnapshot, error)
	Pause(context.Context, string) error
}

// Client calls the lifecycle API and resolved execd endpoints.
type Client struct {
	baseURL    *url.URL
	apiKey     string
	httpClient *http.Client
}

// NewClient creates a lifecycle client.
func NewClient(rawBaseURL, apiKey string, timeout time.Duration) (*Client, error) {
	baseURL, err := url.Parse(strings.TrimRight(rawBaseURL, "/"))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("invalid OpenSandbox base URL %q", rawBaseURL)
	}
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

// ResolveExecdEndpoint asks the lifecycle server for a server-proxied execd URL.
func (c *Client) ResolveExecdEndpoint(ctx context.Context, sandboxID string) (Endpoint, error) {
	path := fmt.Sprintf("/sandboxes/%s/endpoints/%d?use_server_proxy=true", url.PathEscape(sandboxID), execdPort)
	var response struct {
		Endpoint string            `json:"endpoint"`
		Headers  map[string]string `json:"headers"`
	}
	if err := c.requestJSON(ctx, http.MethodGet, c.resolve(path), nil, nil, &response); err != nil {
		return Endpoint{}, err
	}
	endpointURL, err := c.normalizeEndpoint(response.Endpoint)
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{URL: endpointURL, Headers: response.Headers}, nil
}

// Activity reads the execd activity snapshot without updating it.
func (c *Client) Activity(ctx context.Context, endpoint Endpoint) (ActivitySnapshot, error) {
	var snapshot ActivitySnapshot
	if err := c.requestJSON(ctx, http.MethodGet, strings.TrimRight(endpoint.URL, "/")+"/v1/activity", nil, endpoint.Headers, &snapshot); err != nil {
		return ActivitySnapshot{}, err
	}
	return snapshot, nil
}

// Pause requests pause through the lifecycle API.
func (c *Client) Pause(ctx context.Context, sandboxID string) error {
	path := fmt.Sprintf("/sandboxes/%s/pause", url.PathEscape(sandboxID))
	return c.requestJSON(ctx, http.MethodPost, c.resolve(path), nil, nil, nil)
}

func (c *Client) requestJSON(
	ctx context.Context,
	method string,
	requestURL string,
	body any,
	extraHeaders map[string]string,
	response any,
) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return fmt.Errorf("create %s request: %w", method, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("OPEN-SANDBOX-API-KEY", c.apiKey)
	}
	for key, value := range extraHeaders {
		req.Header.Set(key, value)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, requestURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s %s returned %d: %s", method, requestURL, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if response == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
		return fmt.Errorf("decode %s %s response: %w", method, requestURL, err)
	}
	return nil
}

func (c *Client) resolve(path string) string {
	reference, _ := url.Parse(path)
	return c.baseURL.ResolveReference(reference).String()
}

func (c *Client) normalizeEndpoint(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if !strings.Contains(raw, "://") {
		raw = c.baseURL.Scheme + "://" + raw
	}
	endpoint, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid endpoint %q: %w", raw, err)
	}
	if endpoint.Host == "" {
		return "", fmt.Errorf("endpoint %q has no host", raw)
	}
	return endpoint.String(), nil
}

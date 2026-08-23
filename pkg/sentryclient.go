// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// DefaultSentryURL is the base URL of the bborbe Sentry org.
const DefaultSentryURL = "https://bborbe.sentry.io"

// DefaultSentryOrg is the Sentry org slug whose issues the watcher queries.
const DefaultSentryOrg = "bborbe"

// DefaultStatsPeriod is the default activity window for the org issues query.
const DefaultStatsPeriod = "24h"

// sentryPageSize is the max issues per page the org issues list endpoint accepts.
const sentryPageSize = 100

// maxRetries bounds retry attempts on retryable Sentry API responses (429/5xx).
const maxRetries = 3

// backoffBase is the base sleep between retries on transient API failures.
const backoffBase = 5 * time.Second

//counterfeiter:generate -o ../mocks/sentry_client.go --fake-name SentryClient . SentryClient

// SentryClient is the upstream-source surface for the sentry-watcher.
type SentryClient interface {
	// UnresolvedIssues returns the org's unresolved issues with activity in the
	// configured stats period, paged through to exhaustion. Returns an error on
	// request failure, rate-limit exhaustion, or malformed responses — the
	// caller holds the cursor and retries next poll cycle.
	UnresolvedIssues(ctx context.Context) ([]Issue, error)
}

// Issue is the subset of a Sentry org-issues-list entry the watcher consumes.
type Issue struct {
	ID        string  `json:"id"`
	ShortID   string  `json:"shortId"`
	Title     string  `json:"title"`
	LastSeen  string  `json:"lastSeen"`
	Permalink string  `json:"permalink"`
	Project   Project `json:"project"`
}

// Project identifies the Sentry project an issue belongs to.
type Project struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// SentryConfig groups the org-query connection settings for the Sentry client.
type SentryConfig struct {
	URL         string // base URL, e.g. "https://bborbe.sentry.io"
	Org         string // org slug, e.g. "bborbe"
	APIToken    string // Bearer token for the Sentry REST API
	StatsPeriod string // activity window, e.g. "24h"
}

// NewSentryClient returns the production SentryClient backed by the given HTTP
// client and org-query config.
func NewSentryClient(httpClient *http.Client, cfg SentryConfig) SentryClient {
	return &sentryClient{httpClient: httpClient, cfg: cfg}
}

type sentryClient struct {
	httpClient *http.Client
	cfg        SentryConfig
}

func (c *sentryClient) UnresolvedIssues(ctx context.Context) ([]Issue, error) {
	var issues []Issue
	cursor := ""
	for {
		page, next, err := c.fetchPage(ctx, cursor)
		if err != nil {
			return nil, err
		}
		issues = append(issues, page...)
		if next == "" {
			return issues, nil
		}
		cursor = next
	}
}

// fetchPage fetches one page of unresolved issues, following the opaque cursor
// pagination of the Sentry org-issues list endpoint. Retryable statuses (429
// with Retry-After, 5xx) are retried with backoff up to maxRetries; a network
// error is returned immediately (the 24h poll is the retry).
func (c *sentryClient) fetchPage(ctx context.Context, cursor string) ([]Issue, string, error) {
	query := url.Values{}
	query.Set("query", "is:unresolved")
	query.Set("statsPeriod", c.cfg.StatsPeriod)
	query.Set("limit", strconv.Itoa(sentryPageSize))
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	endpoint := fmt.Sprintf(
		"%s/api/0/organizations/%s/issues/?%s",
		c.cfg.URL, c.cfg.Org, query.Encode(),
	)

	var page []Issue
	var next string
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		page, next, lastErr = c.doPage(ctx, endpoint)
		if lastErr == nil {
			return page, next, nil
		}
		he, ok := lastErr.(*sentryHTTPError)
		if !ok {
			// Network / transport error — not retryable within a cycle.
			break
		}
		backoff := sentryBackoff(he.status, he.retryAfter)
		if backoff < 0 {
			break // non-retryable status (4xx except 429)
		}
		if err := sleepCtx(ctx, backoff); err != nil {
			return nil, "", err
		}
	}
	return nil, "", errors.Wrapf(ctx, lastErr, "fetch sentry org issues")
}

func (c *sentryClient) doPage(ctx context.Context, endpoint string) ([]Issue, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, "", errors.Wrapf(ctx, err, "create sentry request %s", endpoint)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", errors.Wrapf(ctx, err, "get %s", endpoint)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			glog.Warningf("close sentry response body: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, "", &sentryHTTPError{status: resp.StatusCode, retryAfter: retryAfter}
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", errors.Wrapf(ctx, err, "read sentry response body")
	}
	var page []Issue
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, "", errors.Wrapf(ctx, err, "unmarshal sentry issues response")
	}
	return page, nextCursor(resp.Header.Get("Link")), nil
}

// IssueDate returns the UTC calendar date (2006-01-02) of the issue's last
// activity. The date feeds the task title and the per-short-id dedup key.
func IssueDate(ctx context.Context, lastSeen string) (string, error) {
	if lastSeen == "" {
		return "", errors.Errorf(ctx, "issue has no lastSeen timestamp")
	}
	t, err := time.Parse(time.RFC3339, lastSeen)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "parse lastSeen %q", lastSeen)
	}
	return t.UTC().Format("2006-01-02"), nil
}

// sentryHTTPError is a non-2xx Sentry API response carrying the retry hint.
type sentryHTTPError struct {
	status     int
	retryAfter time.Duration
}

func (e *sentryHTTPError) Error() string {
	return fmt.Sprintf("sentry API returned status %d", e.status)
}

// sentryBackoff returns the sleep before retrying a failed request, or -1 when
// the status is not retryable. 429 honors Retry-After; 5xx uses the base
// backoff.
func sentryBackoff(status int, retryAfter time.Duration) time.Duration {
	switch {
	case status == http.StatusTooManyRequests:
		if retryAfter > 0 {
			return retryAfter
		}
		return backoffBase
	case status >= 500:
		return backoffBase
	default:
		return -1
	}
}

// parseRetryAfter parses the Retry-After header as whole seconds (the form the
// Sentry API uses). Unparseable → 0 (caller falls back to base backoff).
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	seconds, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// linkCursorPattern extracts the opaque cursor value from a Sentry Link header.
var linkCursorPattern = regexp.MustCompile(`cursor="([^"]+)"`)

// nextCursor returns the cursor for rel="next" from a Link header, or "" when
// there is no next page.
func nextCursor(link string) string {
	for _, part := range strings.Split(link, ",") {
		if !strings.Contains(part, `rel="next"`) {
			continue
		}
		if m := linkCursorPattern.FindStringSubmatch(part); len(m) == 2 {
			return m[1]
		}
	}
	return ""
}

// sleepCtx sleeps for d, aborting early when the context is cancelled.
func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

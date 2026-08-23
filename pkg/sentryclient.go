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
	libtime "github.com/bborbe/time"
	"github.com/golang/glog"
)

// The Default* constants are single configuration defaults, not members of a
// closed enum value set — the go-enum-type/typed-constants-with-collection rule
// targets finite enum-like sets (status codes, kinds, modes); a default value
// has no available-collection to enumerate, so they stay untyped strings like
// every sibling watcher's Default* constants.
const (
	// DefaultSentryURL is the base URL of the bborbe Sentry org.
	DefaultSentryURL = "https://bborbe.sentry.io"

	// DefaultSentryOrg is the Sentry org slug whose issues the watcher queries.
	DefaultSentryOrg = "bborbe"
)

// sentryPageSize is the max issues per page the org issues list endpoint accepts.
const sentryPageSize = 100

// defaultMaxRetries bounds retry attempts on retryable Sentry API responses
// (429/5xx) when SentryConfig.MaxRetries is unset.
const defaultMaxRetries = 3

// defaultBackoff is the base sleep between retries on transient API failures
// when SentryConfig.Backoff is unset.
const defaultBackoff = 5 * time.Second

// maxRetryAfter caps a server-supplied Retry-After header so a misconfigured or
// hostile upstream cannot stall the poll loop for an unbounded period.
const maxRetryAfter = 60 * time.Second

// maxPages bounds the org-issues pagination loop as a backstop against a
// misbehaving upstream that never terminates its Link header.
const maxPages = 1000

//counterfeiter:generate -o ../mocks/sentry_client.go --fake-name SentryClient . SentryClient

// SentryClient is the upstream-source surface for the sentry-watcher.
type SentryClient interface {
	// UnresolvedIssues returns the org's unresolved issues, paged through to
	// exhaustion. Returns an error on request failure, rate-limit exhaustion, or
	// malformed responses — the caller holds the cursor and retries next poll
	// cycle.
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
// APIToken carries display:"length" (inert on a plain config struct — the tag is
// only read by service.Main's argument reflection, which never sees this struct)
// so a future refactor that logs the config masks it; the authoritative masking
// is the display:"length" on the application arg fields that bind SENTRY_API_TOKEN
// (main.go / cmd/run-once main.go), which service.Main applies at startup.
type SentryConfig struct {
	URL      string // base URL, e.g. "https://bborbe.sentry.io"
	Org      string // org slug, e.g. "bborbe"
	APIToken string `display:"length"` // Bearer token for the Sentry REST API

	// MaxRetries bounds retry attempts on retryable responses (429/5xx); 0 → 3.
	MaxRetries int
	// Backoff is the base sleep between retries; 0 → 5s. 429 honors the server's
	// Retry-After header when present (capped at maxRetryAfter).
	Backoff time.Duration

	// CurrentDateTimeGetter supplies timestamps for the fetch audit log
	// (go-time/no-time-now-direct); nil → libtime.NewCurrentDateTime().
	CurrentDateTimeGetter libtime.CurrentDateTimeGetter
}

// NewSentryClient returns the production SentryClient backed by the given HTTP
// client and org-query config, applying defaults for unset knobs.
func NewSentryClient(httpClient *http.Client, cfg SentryConfig) SentryClient {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = defaultMaxRetries
	}
	if cfg.Backoff == 0 {
		cfg.Backoff = defaultBackoff
	}
	if cfg.CurrentDateTimeGetter == nil {
		cfg.CurrentDateTimeGetter = libtime.NewCurrentDateTime()
	}
	return &sentryClient{httpClient: httpClient, cfg: cfg}
}

type sentryClient struct {
	httpClient *http.Client
	cfg        SentryConfig
}

func (c *sentryClient) UnresolvedIssues(ctx context.Context) ([]Issue, error) {
	var issues []Issue
	cursor := ""
	pages := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pages++
		if pages > maxPages {
			return nil, errors.Errorf(ctx, "sentry issues pagination exceeded %d pages", maxPages)
		}
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
// with Retry-After, 5xx) are retried with backoff up to MaxRetries; a network
// error is returned immediately (the 24h poll is the retry).
func (c *sentryClient) fetchPage(ctx context.Context, cursor string) ([]Issue, string, error) {
	query := url.Values{}
	query.Set("query", "is:unresolved")
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
	for attempt := 1; attempt <= c.cfg.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		page, next, lastErr = c.doPage(ctx, endpoint)
		if lastErr == nil {
			return page, next, nil
		}
		he, ok := lastErr.(*sentryHTTPError)
		if !ok {
			// Network / transport error — not retryable within a cycle.
			break
		}
		backoff := sentryBackoff(he.status, he.retryAfter, c.cfg.Backoff)
		if backoff < 0 {
			break // non-retryable status (4xx except 429)
		}
		if glog.V(2) {
			glog.Warningf(
				"sentry issues fetch retry attempt=%d status=%d backoff=%s",
				attempt, he.status, backoff,
			)
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
	start := c.cfg.CurrentDateTimeGetter.Now().Time()
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
		glog.V(2).Infof(
			"sentry issues fetch method=%s path=%s status=%d duration=%s",
			req.Method, req.URL.Path, resp.StatusCode, time.Since(start),
		)
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
	for i := range page {
		sanitizeIssue(&page[i])
	}
	next := nextCursor(resp.Header.Get("Link"))
	glog.V(2).Infof(
		"sentry issues fetch method=%s path=%s status=%d issues=%d next=%q duration=%s",
		req.Method, req.URL.Path, resp.StatusCode, len(page), next, time.Since(start),
	)
	return page, next, nil
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

// sanitizeIssue strips control characters and caps lengths of the untrusted
// Sentry-provided strings so they cannot inject newlines / YAML-significant
// characters into the emitted task title, body, or frontmatter. The go.dev
// source the skeleton ported had only trusted data; Sentry issue titles are
// attacker-controllable within the org.
func sanitizeIssue(issue *Issue) {
	issue.ShortID = sanitizeField(issue.ShortID, 64)
	issue.Title = sanitizeField(issue.Title, 200)
	issue.Permalink = sanitizeField(issue.Permalink, 512)
	issue.Project.Slug = sanitizeField(issue.Project.Slug, 64)
}

// sanitizeField drops control characters (except tab), trims, and caps length.
func sanitizeField(s string, maxLen int) string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	return s
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
// the status is not retryable. 429 honors Retry-After (capped at maxRetryAfter);
// 5xx uses the base backoff.
func sentryBackoff(status int, retryAfter, base time.Duration) time.Duration {
	switch {
	case status == http.StatusTooManyRequests:
		if retryAfter > 0 {
			if retryAfter > maxRetryAfter {
				return maxRetryAfter
			}
			return retryAfter
		}
		return base
	case status >= 500:
		return base
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

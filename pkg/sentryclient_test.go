// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("pkg.SentryClient", func() {
	var (
		ctx         context.Context
		server      *httptest.Server
		handler     func(w http.ResponseWriter, r *http.Request)
		capturedURL string
		mu          sync.Mutex
	)

	BeforeEach(func() {
		ctx = context.Background()
		handler = func(w http.ResponseWriter, _ *http.Request) {}
	})

	JustBeforeEach(func() {
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			capturedURL = r.URL.String()
			mu.Unlock()
			handler(w, r)
		}))
	})

	AfterEach(func() {
		server.Close()
	})

	client := func() pkg.SentryClient {
		return pkg.NewSentryClient(server.Client(), pkg.SentryConfig{
			URL:         server.URL,
			Org:         "bborbe",
			APIToken:    "test-token",
			StatsPeriod: "24h",
		})
	}

	writeIssues := func(w http.ResponseWriter, issues []pkg.Issue, next string) {
		if next != "" {
			w.Header().Set("Link",
				fmt.Sprintf(`<%s/api/0/organizations/bborbe/issues/?cursor=%s>; rel="next"; results="true"; cursor="%s"`,
					server.URL, next, next),
			)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(
			[]byte(
				`[{"id":"1","shortId":"SENTRY-TEST-1","title":"boom","lastSeen":"2026-08-23T04:00:00Z","permalink":"https://bborbe.sentry.io/issues/1/","project":{"slug":"nuke-prod","name":"nuke-prod"}}]`,
			),
		)
	}

	Context("single page of unresolved issues", func() {
		BeforeEach(func() {
			handler = func(w http.ResponseWriter, _ *http.Request) {
				writeIssues(w, nil, "")
			}
		})

		It(
			"queries the org issues endpoint with is:unresolved + statsPeriod and Bearer auth",
			func() {
				issues, err := client().UnresolvedIssues(ctx)
				Expect(err).NotTo(HaveOccurred())
				Expect(issues).To(HaveLen(1))
				Expect(issues[0].ShortID).To(Equal("SENTRY-TEST-1"))
				Expect(issues[0].Project.Slug).To(Equal("nuke-prod"))
			},
		)

		It("sends the query params and the Authorization header", func() {
			_, _ = client().UnresolvedIssues(ctx)
			mu.Lock()
			url := capturedURL
			mu.Unlock()
			Expect(url).To(ContainSubstring("query=is%3Aunresolved"))
			Expect(url).To(ContainSubstring("statsPeriod=24h"))
			Expect(url).To(ContainSubstring("limit=" + strconv.Itoa(100)))
		})
	})

	Context("pagination", func() {
		var calls int
		BeforeEach(func() {
			calls = 0
			handler = func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Query().Get("cursor") == "" {
					w.Header().Set("Link",
						fmt.Sprintf(`<%s/next?cursor=abc>; rel="next"; results="true"; cursor="abc"`, server.URL),
					)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(
					[]byte(
						`[{"id":"1","shortId":"SENTRY-TEST-1","lastSeen":"2026-08-23T04:00:00Z","project":{"slug":"nuke-prod"}}]`,
					),
				)
			}
		})

		It("follows the next cursor to exhaustion", func() {
			issues, err := client().UnresolvedIssues(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(calls).To(Equal(2))
			Expect(issues).To(HaveLen(2))
		})
	})

	Context("rate limit (429) then success", func() {
		var attempts int
		BeforeEach(func() {
			attempts = 0
			handler = func(w http.ResponseWriter, _ *http.Request) {
				attempts++
				if attempts == 1 {
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(http.StatusTooManyRequests)
					return
				}
				writeIssues(w, nil, "")
			}
		})

		It("retries after the rate-limit and succeeds", func() {
			issues, err := client().UnresolvedIssues(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(issues).To(HaveLen(1))
			Expect(attempts).To(Equal(2))
		})
	})

	Context("non-200 status", func() {
		BeforeEach(func() {
			handler = func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		})

		It("returns an error after retries", func() {
			_, err := client().UnresolvedIssues(ctx)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("sentry"))
		})
	})

	Context("non-retryable 4xx status", func() {
		BeforeEach(func() {
			handler = func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			}
		})

		It("returns an error without retrying", func() {
			_, err := client().UnresolvedIssues(ctx)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("malformed JSON", func() {
		BeforeEach(func() {
			handler = func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`not json`))
			}
		})

		It("returns an error", func() {
			_, err := client().UnresolvedIssues(ctx)
			Expect(err).To(HaveOccurred())
		})
	})
})

var _ = Describe("pkg.IssueDate", func() {
	It("extracts the UTC calendar date from an RFC3339 timestamp", func() {
		d, err := pkg.IssueDate(context.Background(), "2026-08-23T04:00:00Z")
		Expect(err).NotTo(HaveOccurred())
		Expect(d).To(Equal("2026-08-23"))
	})

	It("converts to UTC for non-UTC timestamps", func() {
		d, err := pkg.IssueDate(context.Background(), "2026-08-23T23:30:00+02:00")
		Expect(err).NotTo(HaveOccurred())
		Expect(d).To(Equal("2026-08-23"))
	})

	It("errors on an empty timestamp", func() {
		_, err := pkg.IssueDate(context.Background(), "")
		Expect(err).To(HaveOccurred())
	})

	It("errors on a malformed timestamp", func() {
		_, err := pkg.IssueDate(context.Background(), "not-a-time")
		Expect(err).To(HaveOccurred())
	})
})

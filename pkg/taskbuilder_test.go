// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"strings"
	"text/template"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/pkg"
)

// goldenDefaultBody is the exact default body for SENTRY-TEST-1 on 2026-08-23.
// Any change to the default output must be a deliberate edit here too.
const goldenDefaultBody = "# Analyze Sentry issue SENTRY-TEST-1 - 2026-08-23\n\n" +
	"Active unresolved Sentry alert (last activity 2026-08-23) in project nuke-prod.\n" +
	"- Issue: SENTRY-TEST-1\n" +
	"- Project: nuke-prod\n" +
	"- Title: boom\n" +
	"- URL: https://bborbe.sentry.io/issues/1/\n"

var testIssue = pkg.Issue{
	ShortID:   "SENTRY-TEST-1",
	Title:     "boom",
	LastSeen:  "2026-08-23T04:00:00Z",
	Permalink: "https://bborbe.sentry.io/issues/1/",
	Project:   pkg.Project{Slug: "nuke-prod", Name: "nuke-prod"},
}

var _ = Describe("pkg.BuildCreateCommand", func() {
	var ctx context.Context

	mustParse := func(name, text string) *template.Template {
		tmpl, err := template.New(name).Parse(text)
		Expect(err).NotTo(HaveOccurred())
		return tmpl
	}

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("produces the exact sentry-issue-analyzer frontmatter", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx,
			testIssue,
			"2026-08-23",
			pkg.TaskConfig{
				Stage:    "prod",
				Assignee: "sentry-issue-analyzer",
				Status:   "in_progress",
				Phase:    "planning",
			},
		)
		Expect(err).NotTo(HaveOccurred())

		Expect(cmd.Frontmatter["task_type"]).To(Equal("sentry-issue-analyzer"))
		Expect(cmd.Frontmatter["assignee"]).To(Equal("sentry-issue-analyzer"))
		Expect(cmd.Frontmatter["phase"]).To(Equal("planning"))
		Expect(cmd.Frontmatter["status"]).To(Equal("in_progress"))
		Expect(cmd.Frontmatter["stage"]).To(Equal("prod"))
		Expect(
			cmd.Frontmatter["title"],
		).To(Equal("Analyze Sentry issue SENTRY-TEST-1 - 2026-08-23"))
		Expect(cmd.Frontmatter["short_id"]).To(Equal("SENTRY-TEST-1"))
		Expect(cmd.Frontmatter["project"]).To(Equal("nuke-prod"))
		Expect(cmd.Frontmatter["date"]).To(Equal("2026-08-23"))
		Expect(cmd.Frontmatter["issue_url"]).
			To(Equal("https://bborbe.sentry.io/issues/1/"))
		Expect(cmd.Frontmatter["task_identifier"]).
			To(Equal(pkg.DeriveTaskID("SENTRY-TEST-1", "2026-08-23").String()))
		Expect(string(cmd.TaskIdentifier)).To(Equal(cmd.Frontmatter["task_identifier"]))
		Expect(cmd.Title).To(Equal("Analyze Sentry issue SENTRY-TEST-1 - 2026-08-23"))
	})

	It("renders the default body byte-for-byte (golden)", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx, testIssue, "2026-08-23", pkg.TaskConfig{Stage: "dev"},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmd.Body).To(Equal(goldenDefaultBody))
	})

	It("reflects env-configurable assignee/status/phase and a title suffix", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx, testIssue, "2026-08-23",
			pkg.TaskConfig{
				Stage:    "prod",
				Assignee: "me",
				Status:   "next",
				Phase:    "execution",
				Suffix:   "octopus",
			},
		)
		Expect(err).NotTo(HaveOccurred())

		Expect(cmd.Frontmatter["assignee"]).To(Equal("me"))
		Expect(cmd.Frontmatter["status"]).To(Equal("next"))
		Expect(cmd.Frontmatter["phase"]).To(Equal("execution"))
		Expect(cmd.Frontmatter["title"]).
			To(Equal("Analyze Sentry issue SENTRY-TEST-1 - 2026-08-23 - octopus"))
		Expect(cmd.Title).
			To(Equal("Analyze Sentry issue SENTRY-TEST-1 - 2026-08-23 - octopus"))
	})

	It("renders a custom title template", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx, testIssue, "2026-08-23",
			pkg.TaskConfig{
				Stage:         "dev",
				TitleTemplate: mustParse("title", "Sentry {{.ShortID}} on {{.Date}}"),
			},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmd.Title).To(Equal("Sentry SENTRY-TEST-1 on 2026-08-23"))
		Expect(cmd.Frontmatter["title"]).To(Equal("Sentry SENTRY-TEST-1 on 2026-08-23"))
	})

	It("appends the suffix to a custom-templated title", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx, testIssue, "2026-08-23",
			pkg.TaskConfig{
				Stage:         "dev",
				Suffix:        "octopus",
				TitleTemplate: mustParse("title", "Sentry {{.ShortID}} on {{.Date}}"),
			},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmd.Title).To(Equal("Sentry SENTRY-TEST-1 on 2026-08-23 - octopus"))
	})

	It("renders a custom body template using the domain fields", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx, testIssue, "2026-08-23",
			pkg.TaskConfig{
				Stage: "dev",
				BodyTemplate: mustParse(
					"body",
					"{{.ShortID}} in {{.Project}} last seen {{.Date}}: {{.IssueURL}}",
				),
			},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmd.Body).To(Equal(
			"SENTRY-TEST-1 in nuke-prod last seen 2026-08-23: https://bborbe.sentry.io/issues/1/",
		))
	})

	It("default body is an operator-readable header plus the issue URL", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx, testIssue, "2026-08-23", pkg.TaskConfig{Stage: "dev"},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmd.Body).To(ContainSubstring("Analyze Sentry issue SENTRY-TEST-1 - 2026-08-23"))
		Expect(cmd.Body).To(ContainSubstring("https://bborbe.sentry.io/issues/1/"))
		Expect(cmd.Body).NotTo(ContainSubstring("[["))
	})

	It("returns an error when a title template references a missing field", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx, testIssue, "2026-08-23",
			pkg.TaskConfig{Stage: "dev", TitleTemplate: mustParse("title", "{{.DoesNotExist}}")},
		)
		Expect(err).To(HaveOccurred())
		Expect(cmd.Title).To(BeEmpty())
		Expect(cmd.Frontmatter).To(BeNil())
	})

	It("same inputs produce identical commands", func() {
		cfg := pkg.TaskConfig{Stage: "dev"}
		cmd1, err1 := pkg.BuildCreateCommand(ctx, testIssue, "2026-08-23", cfg)
		cmd2, err2 := pkg.BuildCreateCommand(ctx, testIssue, "2026-08-23", cfg)
		Expect(err1).NotTo(HaveOccurred())
		Expect(err2).NotTo(HaveOccurred())
		Expect(cmd1.Frontmatter).To(Equal(cmd2.Frontmatter))
		Expect(cmd1.TaskIdentifier).To(Equal(cmd2.TaskIdentifier))
		Expect(strings.Contains(cmd1.Body, "\n- ")).To(BeTrue()) // header bullets only, not data
	})

	It("ParseTaskTemplate returns nil for empty text (use default)", func() {
		tmpl, err := pkg.ParseTaskTemplate(ctx, "title", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(tmpl).To(BeNil())
	})

	It("ParseTaskTemplate accepts a valid template", func() {
		tmpl, err := pkg.ParseTaskTemplate(ctx, "title", "Sentry {{.ShortID}} on {{.Date}}")
		Expect(err).NotTo(HaveOccurred())
		Expect(tmpl).NotTo(BeNil())
	})

	It("ParseTaskTemplate fails fast on a missing-field template", func() {
		_, err := pkg.ParseTaskTemplate(ctx, "body", "{{.Nope}}")
		Expect(err).To(HaveOccurred())
	})
})

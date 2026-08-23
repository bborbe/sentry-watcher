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

// goldenDefaultBody is the exact default body for go1.27.0 (minor). Any change
// to buildTaskBody's default output must be a deliberate edit here too.
const goldenDefaultBody = "# Update Go to 1.27.0\n\n" +
	"Go 1.27.0 released (minor). Update your Go projects to this version.\n" +
	"- Release notes: https://go.dev/doc/devel/release#go1.27.0\n" +
	"- Downloads: https://go.dev/dl/\n"

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

	It("produces the exact go-version-update frontmatter", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx,
			"go1.27.0",
			"go1.26.5",
			"minor",
			pkg.TaskConfig{
				Stage:    "prod",
				Assignee: "human",
				Status:   "in_progress",
				Phase:    "todo",
			},
		)
		Expect(err).NotTo(HaveOccurred())

		Expect(cmd.Frontmatter["task_type"]).To(Equal("go-version-update"))
		Expect(cmd.Frontmatter["assignee"]).To(Equal("human"))
		Expect(cmd.Frontmatter["phase"]).To(Equal("todo"))
		Expect(cmd.Frontmatter["status"]).To(Equal("in_progress"))
		Expect(cmd.Frontmatter["stage"]).To(Equal("prod"))
		Expect(cmd.Frontmatter["title"]).To(Equal("Update Go to 1.27.0"))
		Expect(cmd.Frontmatter["new_version"]).To(Equal("go1.27.0"))
		Expect(cmd.Frontmatter["previous_version"]).To(Equal("go1.26.5"))
		Expect(cmd.Frontmatter["release_kind"]).To(Equal("minor"))
		Expect(cmd.Frontmatter["release_notes_url"]).
			To(Equal("https://go.dev/doc/devel/release#go1.27.0"))
		Expect(cmd.Frontmatter["task_identifier"]).
			To(Equal(pkg.DeriveTaskID("go1.27.0").String()))
		Expect(string(cmd.TaskIdentifier)).To(Equal(cmd.Frontmatter["task_identifier"]))
		Expect(cmd.Title).To(Equal("Update Go to 1.27.0"))
	})

	It("renders the default body byte-for-byte (golden)", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx,
			"go1.27.0",
			"go1.26.5",
			"minor",
			pkg.TaskConfig{Stage: "dev"},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmd.Body).To(Equal(goldenDefaultBody))
	})

	It("reflects env-configurable assignee/status/phase and a title suffix", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx,
			"go1.27.0",
			"go1.26.5",
			"minor",
			pkg.TaskConfig{
				Stage:    "prod",
				Assignee: "me",
				Status:   "next",
				Phase:    "planning",
				Suffix:   "octopus",
			},
		)
		Expect(err).NotTo(HaveOccurred())

		Expect(cmd.Frontmatter["assignee"]).To(Equal("me"))
		Expect(cmd.Frontmatter["status"]).To(Equal("next"))
		Expect(cmd.Frontmatter["phase"]).To(Equal("planning"))
		Expect(cmd.Frontmatter["title"]).To(Equal("Update Go to 1.27.0 - octopus"))
		Expect(cmd.Title).To(Equal("Update Go to 1.27.0 - octopus"))
	})

	It("renders a custom title template", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx,
			"go1.27.0",
			"go1.26.5",
			"minor",
			pkg.TaskConfig{
				Stage:         "dev",
				TitleTemplate: mustParse("title", "Go {{.Number}} is out"),
			},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmd.Title).To(Equal("Go 1.27.0 is out"))
		Expect(cmd.Frontmatter["title"]).To(Equal("Go 1.27.0 is out"))
	})

	It("appends the suffix to a custom-templated title", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx, "go1.27.0", "go1.26.5", "minor",
			pkg.TaskConfig{
				Stage:         "dev",
				Suffix:        "octopus",
				TitleTemplate: mustParse("title", "Go {{.Number}} is out"),
			},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmd.Title).To(Equal("Go 1.27.0 is out - octopus"))
		Expect(cmd.Frontmatter["title"]).To(Equal("Go 1.27.0 is out - octopus"))
	})

	It("renders a custom body template using Version and ReleaseKind", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx, "go1.27.0", "go1.26.5", "minor",
			pkg.TaskConfig{
				Stage: "dev",
				BodyTemplate: mustParse(
					"body",
					"{{.Version}} is a {{.ReleaseKind}} release (prev {{.PreviousVersion}})",
				),
			},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmd.Body).To(Equal("go1.27.0 is a minor release (prev go1.26.5)"))
	})

	It("default body is vault-agnostic — an operator-readable header plus URLs", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx,
			"go1.27.0",
			"go1.26.5",
			"minor",
			pkg.TaskConfig{Stage: "dev"},
		)
		Expect(err).NotTo(HaveOccurred())

		Expect(cmd.Body).To(ContainSubstring("Go 1.27.0 released (minor)"))
		Expect(cmd.Body).To(ContainSubstring("https://go.dev/doc/devel/release#go1.27.0"))
		Expect(cmd.Body).To(ContainSubstring("https://go.dev/dl/"))
		// The built-in default must stay reusable by anyone — no [[wikilinks]] or
		// deployment-specific references. Vault-specific bodies come from
		// TASK_BODY_TEMPLATE per deployment (e.g. quant).
		Expect(cmd.Body).NotTo(ContainSubstring("[["))
	})

	It("classifies a patch bump title correctly", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx,
			"go1.26.5",
			"go1.26.4",
			"patch",
			pkg.TaskConfig{Stage: "dev"},
		)
		Expect(err).NotTo(HaveOccurred())
		Expect(cmd.Frontmatter["title"]).To(Equal("Update Go to 1.26.5"))
		Expect(cmd.Frontmatter["release_kind"]).To(Equal("patch"))
	})

	It("returns an error when a title template references a missing field", func() {
		cmd, err := pkg.BuildCreateCommand(
			ctx, "go1.27.0", "go1.26.5", "minor",
			pkg.TaskConfig{Stage: "dev", TitleTemplate: mustParse("title", "{{.DoesNotExist}}")},
		)
		Expect(err).To(HaveOccurred())
		Expect(cmd.Title).To(BeEmpty())
		Expect(cmd.Frontmatter).To(BeNil())
	})

	It("rejects an invalid template string at parse time", func() {
		_, err := template.New("title").Parse("{{.Number")
		Expect(err).To(HaveOccurred())
	})

	It("same inputs produce identical commands", func() {
		cfg := pkg.TaskConfig{Stage: "dev"}
		cmd1, err1 := pkg.BuildCreateCommand(ctx, "go1.27.0", "go1.26.5", "minor", cfg)
		cmd2, err2 := pkg.BuildCreateCommand(ctx, "go1.27.0", "go1.26.5", "minor", cfg)
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
		tmpl, err := pkg.ParseTaskTemplate(ctx, "title", "Go {{.Number}} out")
		Expect(err).NotTo(HaveOccurred())
		Expect(tmpl).NotTo(BeNil())
	})

	It("ParseTaskTemplate fails fast on a missing-field template", func() {
		_, err := pkg.ParseTaskTemplate(ctx, "body", "{{.Nope}}")
		Expect(err).To(HaveOccurred())
	})
})

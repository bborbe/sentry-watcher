// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tasktemplate_test

import (
	"bytes"
	"text/template"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/pkg/tasktemplate"
)

// data mirrors the taskbuilder render data for a representative minor release.
type data struct {
	Version         string
	Number          string
	ReleaseKind     string
	PreviousVersion string
	ReleaseNotesURL string
	DownloadsURL    string
}

var sample = data{
	Version:         "go1.27.0",
	Number:          "1.27.0",
	ReleaseKind:     "minor",
	PreviousVersion: "go1.26.5",
	ReleaseNotesURL: "https://go.dev/doc/devel/release#go1.27.0",
	DownloadsURL:    "https://go.dev/dl/",
}

func render(tmpl *template.Template) string {
	var buf bytes.Buffer
	Expect(tmpl.Execute(&buf, sample)).To(Succeed())
	return buf.String()
}

var _ = Describe("tasktemplate defaults", func() {
	It("DefaultTitle renders a single-line title (no trailing newline)", func() {
		out := render(tasktemplate.DefaultTitle)
		Expect(out).To(Equal("Update Go to 1.27.0"))
		Expect(out).NotTo(ContainSubstring("\n"))
	})

	It("DefaultBody renders the deployment-agnostic default", func() {
		out := render(tasktemplate.DefaultBody)
		Expect(out).To(Equal(
			"# Update Go to 1.27.0\n\n" +
				"Go 1.27.0 released (minor). Update your Go projects to this version.\n" +
				"- Release notes: https://go.dev/doc/devel/release#go1.27.0\n" +
				"- Downloads: https://go.dev/dl/\n",
		))
	})

	It("DefaultBody stays vault-agnostic — no wikilinks", func() {
		Expect(render(tasktemplate.DefaultBody)).NotTo(ContainSubstring("[["))
	})
})

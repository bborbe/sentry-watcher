// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package tasktemplate_test

import (
	"bytes"
	"strings"
	"text/template"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/pkg/tasktemplate"
)

// data mirrors the taskbuilder render data for a representative issue.
type data struct {
	ShortID  string
	Date     string
	Title    string
	Project  string
	IssueURL string
}

var sample = data{
	ShortID:  "SENTRY-TEST-1",
	Date:     "2026-08-23",
	Title:    "boom",
	Project:  "nuke-prod",
	IssueURL: "https://bborbe.sentry.io/issues/1/",
}

func render(tmpl *template.Template) string {
	var buf bytes.Buffer
	Expect(tmpl.Execute(&buf, sample)).To(Succeed())
	return buf.String()
}

var _ = Describe("tasktemplate defaults", func() {
	It("DefaultTitle renders a single-line title (no trailing newline)", func() {
		out := render(tasktemplate.DefaultTitle)
		Expect(out).To(Equal("Analyze Sentry issue SENTRY-TEST-1 - 2026-08-23"))
		Expect(out).NotTo(ContainSubstring("\n"))
	})

	It("DefaultBody renders the deployment-agnostic default", func() {
		out := render(tasktemplate.DefaultBody)
		Expect(out).To(Equal(
			"# Analyze Sentry issue SENTRY-TEST-1 - 2026-08-23\n\n" +
				"Active unresolved Sentry alert (last activity 2026-08-23) in project nuke-prod.\n" +
				"- Issue: SENTRY-TEST-1\n" +
				"- Project: nuke-prod\n" +
				"- Title: boom\n" +
				"- URL: https://bborbe.sentry.io/issues/1/\n",
		))
	})

	It("DefaultBody stays vault-agnostic — no wikilinks", func() {
		Expect(render(tasktemplate.DefaultBody)).NotTo(ContainSubstring("[["))
	})
})

// The heartbeat escalation must state the observation (no heartbeat) rather than
// asserting a cause the check has not established. The retention diagnosis was
// wrong for 26 consecutive days while retention worked, and it rendered in three
// places — the title, the body's H1, and the body's prose — so a probe covering
// only the headings would pass while the body still asserted it.
var _ = Describe("tasktemplate heartbeat escalation", func() {
	It("HeartbeatTitle states the missing-heartbeat observation", func() {
		out := render(tasktemplate.HeartbeatTitle)
		Expect(out).To(Equal("Sentry heartbeat missing - 2026-08-23"))
		Expect(out).NotTo(ContainSubstring("retaining events"))
		Expect(out).NotTo(ContainSubstring("not retaining"))
		Expect(out).NotTo(ContainSubstring("\n"))
	})

	It("HeartbeatBody leads with the same observation in its H1", func() {
		Expect(render(tasktemplate.HeartbeatBody)).
			To(HavePrefix("# Sentry heartbeat missing - 2026-08-23\n"))
	})

	It("HeartbeatBody asserts no retention cause in its prose", func() {
		out := render(tasktemplate.HeartbeatBody)
		Expect(out).NotTo(ContainSubstring("retaining events"))
		Expect(out).NotTo(ContainSubstring("not retaining"))
		Expect(out).NotTo(ContainSubstring("The likeliest cause"))
	})

	It("HeartbeatBody orders the three candidates cheapest-first", func() {
		out := render(tasktemplate.HeartbeatBody)
		emitter := strings.Index(out, "Is the emitter running?")
		proxy := strings.Index(out, "Is `sentry-proxy` forwarding?")
		quota := strings.Index(out, "Is the org over its free-tier cap?")
		Expect(emitter).To(BeNumerically(">", -1))
		Expect(proxy).To(BeNumerically(">", emitter))
		Expect(quota).To(BeNumerically(">", proxy))
	})
})

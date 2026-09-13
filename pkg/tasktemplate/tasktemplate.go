// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package tasktemplate holds the built-in default title/body templates for the
// emitted sentry-issue-analyzer task, authored as embedded markdown files. A
// deployment overrides them via the TASK_TITLE_TEMPLATE / TASK_BODY_TEMPLATE
// envs; these defaults are deployment-agnostic so the watcher is reusable as-is.
//
// The templates are Go text/templates rendered against the task-template data
// (fields: ShortID, Date, Title, Project, IssueURL).
package tasktemplate

import (
	_ "embed"
	"strings"
	"text/template"
)

//go:embed default-title.md
var defaultTitle string

//go:embed default-body.md
var defaultBody string

// DefaultTitle is the built-in title template used when no TASK_TITLE_TEMPLATE
// override is configured. Trailing newlines from the embedded file are trimmed
// so the rendered title stays a single line.
var DefaultTitle = template.Must(
	template.New("title").Parse(strings.TrimRight(defaultTitle, "\n")),
)

// DefaultBody is the built-in body template used when no TASK_BODY_TEMPLATE
// override is configured.
var DefaultBody = template.Must(template.New("body").Parse(defaultBody))

//go:embed heartbeat-title.md
var heartbeatTitle string

//go:embed heartbeat-body.md
var heartbeatBody string

// HeartbeatTitle is the title template for the alert task emitted when the
// zero-events heartbeat stops arriving. It is deliberately not the analyze-task
// title: this task reports an *absence*, so it links to no Sentry issue and
// must not read as one.
var HeartbeatTitle = template.Must(
	template.New("heartbeat-title").Parse(strings.TrimRight(heartbeatTitle, "\n")),
)

// HeartbeatBody is the body template for the heartbeat alert task — the
// cheapest-first triage order, so the reader does not start by assuming
// ingestion is dead when a stopped emitter explains the same absence.
var HeartbeatBody = template.Must(
	template.New("heartbeat-body").Parse(heartbeatBody),
)

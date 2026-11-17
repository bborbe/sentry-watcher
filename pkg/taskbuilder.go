// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"text/template"

	agentlib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/errors"

	"github.com/bborbe/sentry-watcher/pkg/tasktemplate"
)

// TaskTypeSentryIssueAnalyzer is the `task_type` this watcher emits. It is a
// task type, not an agent name: the executor resolves an agent Config by
// `assignee`, so this value must never be used as one.
const TaskTypeSentryIssueAnalyzer = "sentry-issue-analyzer"

// DefaultTaskAssignee is the agent name the deployed `sentry-analyzer-agent`
// Config routes these records to (its `spec.assignee`, and a member of its
// `spec.taskTypes`).
//
// It must stay an agent name. An assignee naming a task type resolves no
// Config, so the executor skips every emitted record and increments
// agent_executor_skipped_unknown_assignee_total — the AgentUnknownAssignee
// alert, cleared by hand three times before the default was fixed.
const DefaultTaskAssignee = "sentry-analyzer-agent"

// TaskConfig groups per-task envelope settings (stage routing + emitted-task
// frontmatter knobs).
type TaskConfig struct {
	Stage string // "dev" or "prod" — frontmatter `stage`
	// Assignee is the frontmatter `assignee`. The binaries default it to
	// DefaultTaskAssignee; it must name a deployed agent Config, never a task
	// type.
	Assignee string
	Status   string // frontmatter `status` (default "in_progress")
	Phase    string // frontmatter `phase` (default "planning")
	Suffix   string // optional title/filename suffix appended as " - <suffix>"; empty = none

	// TitleTemplate overrides the emitted-task title; nil ⇒ tasktemplate.DefaultTitle.
	TitleTemplate *template.Template
	// BodyTemplate overrides the emitted-task body; nil ⇒ tasktemplate.DefaultBody.
	BodyTemplate *template.Template
}

// taskTemplateData is the data passed to the title/body text/templates.
type taskTemplateData struct {
	ShortID  string // Sentry short-id, e.g. "SENTRY-TEST-1"
	Date     string // UTC calendar date of last activity, "2006-01-02"
	Title    string // Sentry issue title
	Project  string // Sentry project slug, e.g. "nuke-prod"
	IssueURL string // Sentry issue permalink
}

// BuildCreateCommand assembles the CreateTaskCommand for an active Sentry
// issue. issue is the source alert; date is the UTC calendar date of its last
// activity (see IssueDate). The title and body are rendered from
// cfg.TitleTemplate / cfg.BodyTemplate (or the package defaults when nil); any
// template-execution failure is returned as a wrapped error.
func BuildCreateCommand(
	ctx context.Context,
	issue Issue,
	date string,
	cfg TaskConfig,
) (task.CreateCommand, error) {
	taskIDStr := DeriveTaskID(issue.ShortID).String()
	data := taskTemplateData{
		ShortID:  issue.ShortID,
		Date:     date,
		Title:    issue.Title,
		Project:  issue.Project.Slug,
		IssueURL: issue.Permalink,
	}

	renderedTitle, err := renderTemplate(ctx, cfg.TitleTemplate, tasktemplate.DefaultTitle, data)
	if err != nil {
		return task.CreateCommand{}, errors.Wrapf(ctx, err, "render task title")
	}
	title := applySuffix(renderedTitle, cfg.Suffix)

	body, err := renderTemplate(ctx, cfg.BodyTemplate, tasktemplate.DefaultBody, data)
	if err != nil {
		return task.CreateCommand{}, errors.Wrapf(ctx, err, "render task body")
	}

	return task.CreateCommand{
		Title:          title,
		TaskIdentifier: agentlib.TaskIdentifier(taskIDStr),
		Frontmatter: buildFrontmatter(
			issue.ShortID,
			issue.Project.Slug,
			issue.Permalink,
			date,
			taskIDStr,
			title,
			cfg,
		),
		Body: body,
	}, nil
}

// sampleTemplateData is a representative taskTemplateData used to validate a
// configured template at startup: executing against it surfaces missing-field
// references (which parse cleanly but fail at render time) before the first poll.
var sampleTemplateData = taskTemplateData{
	ShortID:  "SENTRY-TEST-1",
	Date:     "2026-01-01",
	Title:    "Example unresolved error",
	Project:  "nuke-prod",
	IssueURL: "https://bborbe.sentry.io/organizations/bborbe/issues/1/",
}

// ParseTaskTemplate parses text as a named Go text/template and validates it by
// rendering against sample data, so a template referencing an unknown field
// fails fast at startup rather than silently skipping every emit. Empty text
// returns (nil, nil) ⇒ the built-in default is used.
//
// Paired with ParseTaskTemplateDefault per go-parse/paired-parse-and-parsedefault.
func ParseTaskTemplate(ctx context.Context, name, text string) (*template.Template, error) {
	if text == "" {
		return nil, nil
	}
	tmpl, err := template.New(name).Parse(text)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "parse %s template %q", name, text)
	}
	if _, err := renderTemplate(ctx, tmpl, tmpl, sampleTemplateData); err != nil {
		return nil, errors.Wrapf(ctx, err, "validate %s template %q", name, text)
	}
	return tmpl, nil
}

// ParseTaskTemplateDefault parses text as a named Go text/template (same core
// logic as ParseTaskTemplate) but returns the fallback template on parse or
// validation failure instead of an error — for call sites that prefer a safe
// default over fail-fast. Paired with ParseTaskTemplate per
// go-parse/paired-parse-and-parsedefault.
func ParseTaskTemplateDefault(
	ctx context.Context,
	name, text string,
	fallback *template.Template,
) *template.Template {
	tmpl, err := ParseTaskTemplate(ctx, name, text)
	if err != nil {
		return fallback
	}
	return tmpl
}

// renderTemplate executes tmpl (or fallback when tmpl is nil) against data and
// returns the rendered string, wrapping any execution error.
func renderTemplate(
	ctx context.Context,
	tmpl *template.Template,
	fallback *template.Template,
	data taskTemplateData,
) (string, error) {
	if tmpl == nil {
		tmpl = fallback
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", errors.Wrapf(ctx, err, "execute template %q", tmpl.Name())
	}
	return buf.String(), nil
}

func buildFrontmatter(
	shortID string,
	projectSlug string,
	issueURL string,
	date string,
	taskIDStr string,
	title string,
	cfg TaskConfig,
) agentlib.TaskFrontmatter {
	return agentlib.TaskFrontmatter{
		"task_type":       TaskTypeSentryIssueAnalyzer,
		"assignee":        cfg.Assignee,
		"phase":           cfg.Phase,
		"status":          cfg.Status,
		"stage":           cfg.Stage,
		"task_identifier": taskIDStr,
		"title":           title,
		"short_id":        shortID,
		"project":         projectSlug,
		"date":            date,
		"issue_url":       issueURL,
	}
}

// applySuffix appends " - <suffix>" to the rendered title when suffix is
// non-empty (feeding both the title frontmatter and the derived filename).
func applySuffix(title string, suffix string) string {
	if suffix != "" {
		return title + " - " + suffix
	}
	return title
}

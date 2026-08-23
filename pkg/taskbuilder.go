// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"bytes"
	"context"
	"strings"
	"text/template"

	agentlib "github.com/bborbe/agent"
	task "github.com/bborbe/agent/command/task"
	"github.com/bborbe/errors"

	"github.com/bborbe/sentry-watcher/pkg/tasktemplate"
)

// TaskConfig groups per-task envelope settings (stage routing + emitted-task
// frontmatter knobs).
type TaskConfig struct {
	Stage    string // "dev" or "prod" — frontmatter `stage`
	Assignee string // frontmatter `assignee` (default "human")
	Status   string // frontmatter `status` (default "in_progress")
	Phase    string // frontmatter `phase` (default "todo")
	Suffix   string // optional title/filename suffix appended as " - <suffix>"; empty = none

	// TitleTemplate overrides the emitted-task title; nil ⇒ tasktemplate.DefaultTitle.
	TitleTemplate *template.Template
	// BodyTemplate overrides the emitted-task body; nil ⇒ tasktemplate.DefaultBody.
	BodyTemplate *template.Template
}

// taskTemplateData is the data passed to the title/body text/templates.
type taskTemplateData struct {
	Version         string // canonical go-version string, e.g. "go1.26.5"
	Number          string // version number without the "go" prefix, e.g. "1.26.5"
	ReleaseKind     string // "minor" or "patch"
	PreviousVersion string // previous canonical go-version string, e.g. "go1.26.4"
	ReleaseNotesURL string // full go.dev release-notes anchor URL
	DownloadsURL    string // go.dev downloads page URL
}

// releaseNotesBaseURL is the go.dev release-notes page; the version string is
// appended as an anchor (e.g. #go1.27.0).
const releaseNotesBaseURL = "https://go.dev/doc/devel/release#"

// downloadsURL is the go.dev downloads page.
const downloadsURL = "https://go.dev/dl/"

// BuildCreateCommand assembles the CreateTaskCommand for a new Go version.
// newVersion and previousVersion are canonical go-version strings (e.g.
// "go1.27.0"); releaseKind is "minor" or "patch". The title and body are
// rendered from cfg.TitleTemplate / cfg.BodyTemplate (or the package defaults
// when nil); any template-execution failure is returned as a wrapped error.
func BuildCreateCommand(
	ctx context.Context,
	newVersion string,
	previousVersion string,
	releaseKind string,
	cfg TaskConfig,
) (task.CreateCommand, error) {
	taskIDStr := DeriveTaskID(newVersion).String()
	data := taskTemplateData{
		Version:         newVersion,
		Number:          strings.TrimPrefix(newVersion, "go"),
		ReleaseKind:     releaseKind,
		PreviousVersion: previousVersion,
		ReleaseNotesURL: releaseNotesBaseURL + newVersion,
		DownloadsURL:    downloadsURL,
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
			newVersion,
			previousVersion,
			releaseKind,
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
	Version:         "go1.0.0",
	Number:          "1.0.0",
	ReleaseKind:     "patch",
	PreviousVersion: "go0.9.0",
	ReleaseNotesURL: releaseNotesBaseURL + "go1.0.0",
	DownloadsURL:    downloadsURL,
}

// ParseTaskTemplate parses text as a named Go text/template and validates it by
// rendering against sample data, so a template referencing an unknown field
// fails fast at startup rather than silently skipping every emit. Empty text
// returns (nil, nil) ⇒ the built-in default is used.
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
	newVersion string,
	previousVersion string,
	releaseKind string,
	taskIDStr string,
	title string,
	cfg TaskConfig,
) agentlib.TaskFrontmatter {
	return agentlib.TaskFrontmatter{
		"task_type":         "go-version-update",
		"assignee":          cfg.Assignee,
		"phase":             cfg.Phase,
		"status":            cfg.Status,
		"stage":             cfg.Stage,
		"task_identifier":   taskIDStr,
		"title":             title,
		"new_version":       newVersion,
		"previous_version":  previousVersion,
		"release_kind":      releaseKind,
		"release_notes_url": releaseNotesBaseURL + newVersion,
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

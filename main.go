// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Command sentry-watcher polls the bborbe Sentry org for unresolved issues with
// recent activity and publishes one CreateTaskCommand per (short-id, date) so
// the sentry-issue-analyzer agent can analyze each active alert.
//
// See [[Watcher Writing Guide]] for the producer-side contract; [[Agent Task
// File Contract]] for the frontmatter/body shape this watcher emits.
package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/bborbe/cron"
	"github.com/bborbe/cqrs/base"
	"github.com/bborbe/errors"
	libhttp "github.com/bborbe/http"
	libkafka "github.com/bborbe/kafka"
	"github.com/bborbe/log"
	"github.com/bborbe/run"
	libsentry "github.com/bborbe/sentry"
	"github.com/bborbe/service"
	"github.com/golang/glog"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bborbe/sentry-watcher/pkg"
	"github.com/bborbe/sentry-watcher/pkg/factory"
	"github.com/bborbe/sentry-watcher/pkg/handler"
)

// httpClientTimeout bounds each Sentry API request.
const httpClientTimeout = 30 * time.Second

func main() {
	app := &application{}
	os.Exit(service.Main(context.Background(), app, &app.SentryDSN, &app.SentryProxy))
}

type application struct {
	SentryDSN   string `required:"false" arg:"sentry-dsn"   env:"SENTRY_DSN"   usage:"SentryDSN"    display:"length"`
	SentryProxy string `required:"false" arg:"sentry-proxy" env:"SENTRY_PROXY" usage:"Sentry Proxy"`

	Listen       string           `required:"false" arg:"listen"        env:"LISTEN"        usage:"HTTP listen address (healthz/readiness/metrics)"                                                                                                                                                                            default:":9090"`
	Stage        string           `required:"true"  arg:"stage"         env:"STAGE"         usage:"Deployment stage (dev|prod)"`
	CronExpression string          `required:"false" arg:"cron-expression" env:"CRON_EXPRESSION" usage:"Cron expression (6-field, seconds-first) determining when the scan runs; uses the pod's local timezone (UTC by default)"                                                                                                default:"0 0 0 * * ?"`
	CursorPath   string           `required:"false" arg:"cursor-path"   env:"CURSOR_PATH"   usage:"Cursor persistence path (mount a PVC)"                                                                                                                                                                                      default:"/data/cursor.json"`
	SeedDate     string           `required:"false" arg:"seed-date"     env:"SEED_DATE"     usage:"Optional UTC date (2006-01-02) to seed the cursor with on cold-start instead of the current state; makes the first poll emit a task for every in-scope active issue dated after it. Empty = seed to current, emit nothing."`
	KafkaBrokers libkafka.Brokers `required:"true"  arg:"kafka-brokers" env:"KAFKA_BROKERS" usage:"Comma-separated Kafka broker list"`

	SentryURL      string `required:"false" arg:"sentry-url"       env:"SENTRY_URL"       usage:"Base URL of the Sentry org"                                                         default:"https://bborbe.sentry.io"`
	SentryOrg      string `required:"false" arg:"sentry-org"       env:"SENTRY_ORG"       usage:"Sentry org slug"                                                                    default:"bborbe"`
	SentryAPIToken string `required:"true"  arg:"sentry-api-token" env:"SENTRY_API_TOKEN" usage:"Sentry REST API Bearer token"                                                                                               display:"length"`
	SentryProjects string `required:"true"  arg:"sentry-projects"  env:"SENTRY_PROJECTS"  usage:"Comma-separated per-cluster project slug allowlist (hell,nuke-dev,nuke-prod,quant)" default:"hell,nuke-dev,nuke-prod,quant"`

	TaskAssignee string `required:"true"  arg:"task-assignee" env:"TASK_ASSIGNEE" usage:"Frontmatter assignee for published sentry-issue-analyzer tasks"                                                                                                  default:"sentry-issue-analyzer"`
	TaskStatus   string `required:"true"  arg:"task-status"   env:"TASK_STATUS"   usage:"Frontmatter status for published sentry-issue-analyzer tasks"                                                                                                    default:"in_progress"`
	TaskPhase    string `required:"true"  arg:"task-phase"    env:"TASK_PHASE"    usage:"Frontmatter phase for published sentry-issue-analyzer tasks"                                                                                                     default:"planning"`
	TaskSuffix   string `required:"false" arg:"task-suffix"   env:"TASK_SUFFIX"   usage:"Optional suffix appended to sentry task titles/filenames as ' - suffix'; empty = no suffix. Use distinct values per deployment to prevent task-file collisions."`
	TargetVault  string `required:"false" arg:"target-vault"  env:"TARGET_VAULT"  usage:"Obsidian vault slug the task-controller materializes the task into (e.g. 'personal', 'openclaw'). Empty = controller default (openclaw)."`

	TaskTitleTemplate string `required:"false" arg:"task-title-template" env:"TASK_TITLE_TEMPLATE" usage:"Go text/template overriding the emitted task title; empty = built-in default 'Analyze Sentry issue {{.ShortID}} - {{.Date}}'. Fields: ShortID, Date, Title, Project, IssueURL."`
	TaskBodyTemplate  string `required:"false" arg:"task-body-template"  env:"TASK_BODY_TEMPLATE"  usage:"Go text/template overriding the emitted task body; empty = built-in default body. Fields: ShortID, Date, Title, Project, IssueURL."`

	// TopicPrefix selects the Kafka topic prefix used for CQRS topic construction
	// (e.g. "develop" / "master"); independent of Stage. Empty means unprefixed topics.
	TopicPrefix base.TopicPrefix `required:"false" arg:"topic-prefix" env:"TOPIC_PREFIX" usage:"Kafka topic prefix for CQRS topic construction"`
}

func (a *application) Run(ctx context.Context, _ libsentry.Client) error {
	// Fail fast on a malformed expression rather than surfacing it from inside
	// the cron loop at the first schedule tick. CreateDefaultParser expects
	// 6-field, seconds-first expressions (e.g. "0 0 0 * * ?"); a 5-field
	// "0 0 * * *" silently means "every hour at second 0".
	if _, err := cron.CreateDefaultParser().Parse(a.CronExpression); err != nil {
		return errors.Wrapf(ctx, err, "parse cron expression %q", a.CronExpression)
	}

	if a.SeedDate != "" {
		if _, err := time.Parse("2006-01-02", a.SeedDate); err != nil {
			return errors.Wrapf(ctx, err, "parse seed date %q", a.SeedDate)
		}
	}

	projects := splitProjects(a.SentryProjects)
	if len(projects) == 0 {
		return errors.Errorf(ctx, "SENTRY_PROJECTS must list at least one project slug")
	}

	titleTemplate, err := pkg.ParseTaskTemplate(ctx, "title", a.TaskTitleTemplate)
	if err != nil {
		return errors.Wrapf(ctx, err, "task title template")
	}
	bodyTemplate, err := pkg.ParseTaskTemplate(ctx, "body", a.TaskBodyTemplate)
	if err != nil {
		return errors.Wrapf(ctx, err, "task body template")
	}

	syncProducer, err := libkafka.NewSyncProducerWithName(ctx, a.KafkaBrokers, "sentry-watcher")
	if err != nil {
		return errors.Wrap(ctx, err, "create sync producer")
	}
	defer func() {
		if cerr := syncProducer.Close(); cerr != nil {
			glog.Warningf("close kafka sync producer: %v", cerr)
		}
	}()

	httpClient := &http.Client{Timeout: httpClientTimeout}
	metrics := pkg.NewMetrics(nil)
	sender := factory.CreateKafkaSender(syncProducer, a.TopicPrefix, a.TargetVault)
	w := factory.CreateWatcher(
		httpClient,
		sender,
		a.CursorPath,
		metrics,
		pkg.TaskConfig{
			Stage:         a.Stage,
			Assignee:      a.TaskAssignee,
			Status:        a.TaskStatus,
			Phase:         a.TaskPhase,
			Suffix:        a.TaskSuffix,
			TitleTemplate: titleTemplate,
			BodyTemplate:  bodyTemplate,
		},
		projects,
		a.SeedDate,
		pkg.SentryConfig{
			URL:      a.SentryURL,
			Org:      a.SentryOrg,
			APIToken: a.SentryAPIToken,
		},
	)

	glog.V(2).Infof(
		"sentry-watcher starting stage=%s cron=%s listen=%s",
		a.Stage, a.CronExpression, a.Listen,
	)

	// Wall-clock schedule via bborbe/cron (real cron expression, not an
	// interval ticker that drifts to the last pod restart). The first scan
	// fires at the next match; manual scans go through /trigger.
	cronJob := cron.NewExpressionCron(
		cron.Expression(a.CronExpression),
		run.Func(func(ctx context.Context) error {
			return w.Poll(ctx)
		}),
	)

	return run.CancelOnFirstFinish(ctx,
		run.Func(cronJob.Run),
		a.createHTTPServer(w),
	)
}

// splitProjects splits and trims the comma-separated project allowlist env.
func splitProjects(s string) []string {
	var projects []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			projects = append(projects, p)
		}
	}
	return projects
}

// createHTTPServer serves the mandatory triple (/healthz, /readiness, /metrics)
// plus the canonical admin surface: /setloglevel/{level} (runtime log-verbosity
// flip), /gc (heap GC), /resetcursor (delete the cursor → next poll cold-starts)
// and /setcursor/{shortId}/{date} (pin a short-id's last emitted date), both
// dangerous-wrapped to require a passphrase, plus /trigger which forces one
// immediate Poll. The watcher instance is shared with the poll loop so /trigger
// drives the same collaborators.
func (a *application) createHTTPServer(w pkg.Watcher) run.Func {
	return func(ctx context.Context) error {
		router := mux.NewRouter()
		router.Path("/healthz").Handler(libhttp.NewPrintHandler("OK"))
		router.Path("/readiness").Handler(libhttp.NewPrintHandler("OK"))
		router.Path("/metrics").Handler(promhttp.Handler())
		router.Path("/setloglevel/{level}").
			Handler(log.NewSetLoglevelHandler(ctx, log.NewLogLevelSetter(2, 5*time.Minute)))
		router.Path("/gc").Handler(libhttp.NewGarbageCollectorHandler())
		router.Path("/resetcursor").
			Handler(libhttp.NewDangerousHandlerWrapper(
				libhttp.NewErrorHandler(handler.NewResetCursorHandler(a.CursorPath))))
		router.Path("/setcursor/{shortId}/{date}").
			Handler(libhttp.NewDangerousHandlerWrapper(
				libhttp.NewErrorHandler(
					handler.NewSetCursorHandler(pkg.NewFileCursorStore(a.CursorPath)),
				),
			))
		router.Path("/trigger").Handler(libhttp.NewErrorHandler(handler.NewTriggerHandler(w)))
		glog.V(2).Infof("http server listening on %s", a.Listen)
		return libhttp.NewServer(a.Listen, router).Run(ctx)
	}
}


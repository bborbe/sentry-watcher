// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import "github.com/prometheus/client_golang/prometheus"

//counterfeiter:generate -o ../mocks/metrics.go --fake-name Metrics . Metrics

// Metrics is the observable counter surface required by [[Watcher Writing Guide]]
// § Required observability.
type Metrics interface {
	// IncPollCycle — result: "success" | "go_dev_error" | "image_check_error" | "build_error"
	IncPollCycle(result string)

	// IncPublished — status: "create" | "error"
	IncPublished(status string)

	// IncFilterSkipped — reason: "version_unchanged" | "image_not_ready"
	IncFilterSkipped(reason string)
}

const metricNamespace = "go_version_watcher"

// NewMetrics returns the Prometheus-backed Metrics implementation registered
// against the supplied Registerer. Pass nil for the default registry.
// Pre-initialises every label combination so Prometheus exposes a zero series
// before the first event fires.
func NewMetrics(registerer prometheus.Registerer) Metrics {
	if registerer == nil {
		registerer = prometheus.DefaultRegisterer
	}
	m := &prometheusMetrics{
		pollCycleTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "poll_cycle_total",
			Help:      "Total poll cycles by result.",
		}, []string{"result"}),
		publishedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "published_total",
			Help:      "Total task-publish attempts by status.",
		}, []string{"status"}),
		filterSkippedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: metricNamespace,
			Name:      "filter_skipped_total",
			Help:      "Total cycles filtered out by reason.",
		}, []string{"reason"}),
	}
	registerer.MustRegister(
		m.pollCycleTotal,
		m.publishedTotal,
		m.filterSkippedTotal,
	)
	for _, r := range []string{"success", "go_dev_error", "image_check_error", "build_error"} {
		m.pollCycleTotal.WithLabelValues(r).Add(0)
	}
	for _, s := range []string{"create", "error"} {
		m.publishedTotal.WithLabelValues(s).Add(0)
	}
	for _, r := range []string{"version_unchanged", "image_not_ready"} {
		m.filterSkippedTotal.WithLabelValues(r).Add(0)
	}
	return m
}

type prometheusMetrics struct {
	pollCycleTotal     *prometheus.CounterVec
	publishedTotal     *prometheus.CounterVec
	filterSkippedTotal *prometheus.CounterVec
}

func (m *prometheusMetrics) IncPollCycle(result string) {
	m.pollCycleTotal.WithLabelValues(result).Inc()
}

func (m *prometheusMetrics) IncPublished(status string) {
	m.publishedTotal.WithLabelValues(status).Inc()
}

func (m *prometheusMetrics) IncFilterSkipped(reason string) {
	m.filterSkippedTotal.WithLabelValues(reason).Inc()
}

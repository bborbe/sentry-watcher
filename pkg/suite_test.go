// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/format"
)

// TestPkg is the single Ginkgo suite entry point for package pkg_test — it runs
// every Describe spec in this package's _test files (watcher_test.go,
// sentryclient_test.go, taskbuilder_test.go, taskid_test.go, cursor_test.go,
// taskpublisher_test.go) in one RunSpecs call, per the standard Ginkgo
// convention (one suite per package; sibling watchers e.g. github-pr-watcher
// pkg/suite_test.go use the identical layout). A second RunSpecs call in this
// package would panic — do not add per-spec-file suite files here.
func TestPkg(t *testing.T) {
	time.Local = time.UTC
	format.TruncatedDiff = false
	gomega.RegisterFailHandler(ginkgo.Fail)
	suiteConfig, reporterConfig := ginkgo.GinkgoConfiguration()
	suiteConfig.Timeout = 60 * time.Second
	ginkgo.RunSpecs(t, "Pkg Suite", suiteConfig, reporterConfig)
}

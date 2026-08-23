// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg_test

import (
	"context"
	"errors"

	task "github.com/bborbe/agent/command/task"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/bborbe/sentry-watcher/mocks"
	"github.com/bborbe/sentry-watcher/pkg"
)

var _ = Describe("pkg.TaskPublisher", func() {
	var cmd task.CreateCommand

	BeforeEach(func() {
		var err error
		cmd, err = pkg.BuildCreateCommand(
			context.Background(), testIssue, "2026-08-23", pkg.TaskConfig{Stage: "dev"},
		)
		Expect(err).NotTo(HaveOccurred())
	})

	It("returns true and calls IncPublished(\"create\") on send success", func() {
		sender := new(mocks.TaskCreateCommandSender)
		fakeMetrics := new(mocks.Metrics)
		publisher := pkg.NewTaskPublisher(sender, fakeMetrics)

		result := publisher.PublishCreate(context.Background(), cmd)

		Expect(result).To(BeTrue())
		Expect(fakeMetrics.IncPublishedCallCount()).To(Equal(1))
		Expect(fakeMetrics.IncPublishedArgsForCall(0)).To(Equal("create"))
		Expect(sender.SendCommandCallCount()).To(Equal(1))
		_, sentCmd := sender.SendCommandArgsForCall(0)
		Expect(sentCmd.Frontmatter["task_type"]).To(Equal("sentry-issue-analyzer"))
	})

	It("returns false and calls IncPublished(\"error\") on send failure", func() {
		sender := new(mocks.TaskCreateCommandSender)
		sender.SendCommandReturns(errors.New("kafka send failed"))
		fakeMetrics := new(mocks.Metrics)
		publisher := pkg.NewTaskPublisher(sender, fakeMetrics)

		result := publisher.PublishCreate(context.Background(), cmd)

		Expect(result).To(BeFalse())
		Expect(fakeMetrics.IncPublishedCallCount()).To(Equal(1))
		Expect(fakeMetrics.IncPublishedArgsForCall(0)).To(Equal("error"))
	})
})

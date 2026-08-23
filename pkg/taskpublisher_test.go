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

type fakeCreateCommandSender struct {
	sendErr      error
	capturedCmds []task.CreateCommand
}

func (f *fakeCreateCommandSender) SendCommand(_ context.Context, cmd task.CreateCommand) error {
	f.capturedCmds = append(f.capturedCmds, cmd)
	return f.sendErr
}

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
		fakeSender := &fakeCreateCommandSender{}
		fakeMetrics := new(mocks.Metrics)
		publisher := pkg.NewTaskPublisher(fakeSender, fakeMetrics)

		result := publisher.PublishCreate(context.Background(), cmd)

		Expect(result).To(BeTrue())
		Expect(fakeMetrics.IncPublishedCallCount()).To(Equal(1))
		Expect(fakeMetrics.IncPublishedArgsForCall(0)).To(Equal("create"))
		Expect(fakeSender.capturedCmds).To(HaveLen(1))
		Expect(
			fakeSender.capturedCmds[0].Frontmatter["task_type"],
		).To(Equal("sentry-issue-analyzer"))
	})

	It("returns false and calls IncPublished(\"error\") on send failure", func() {
		fakeSender := &fakeCreateCommandSender{sendErr: errors.New("kafka send failed")}
		fakeMetrics := new(mocks.Metrics)
		publisher := pkg.NewTaskPublisher(fakeSender, fakeMetrics)

		result := publisher.PublishCreate(context.Background(), cmd)

		Expect(result).To(BeFalse())
		Expect(fakeMetrics.IncPublishedCallCount()).To(Equal(1))
		Expect(fakeMetrics.IncPublishedArgsForCall(0)).To(Equal("error"))
	})
})

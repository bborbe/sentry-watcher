// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/google/uuid"
)

// taskIDNamespace is the UUID5 namespace for sentry-issue-analyzer tasks.
// Stable across releases — changing it would break controller dedup.
var taskIDNamespace = uuid.MustParse("1b06ab0c-eede-481f-b6ee-e91b0708fba9")

// DeriveTaskID returns a UUID5 derived deterministically from the short-id
// alone (e.g. "SENTRY-TEST-1").
//
// Uniqueness set rationale (per [[Watcher Writing Guide]] § Deterministic
// task_identifier):
//   - Same short-id → same task_id on every day the issue is active, so the
//     controller's identifier index resolves a later emit to the existing
//     file. A task still open in human_review is therefore never duplicated
//     by the next day's emit.
//   - The controller materializes a fresh non-terminal task in place when the
//     file it resolves to is terminal ("completed"/"aborted"), so an issue
//     that recurs after its analysis finished is analysed again without a
//     second file.
//   - A different issue → different short-id → different task_id.
//
// The date is deliberately NOT part of the key: keying on (short-id, date)
// produced one task per issue per active day, which is the duplicate flood this
// watcher used to emit.
func DeriveTaskID(shortID string) uuid.UUID {
	return uuid.NewSHA1(taskIDNamespace, []byte("sentry:"+shortID))
}

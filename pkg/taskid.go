// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"github.com/google/uuid"
)

// taskIDNamespace is the UUID5 namespace for go-version-update tasks.
// Stable across releases — changing it would break controller dedup.
var taskIDNamespace = uuid.MustParse("800ae6f3-d5ac-47e8-a330-958561a4e1ad")

// DeriveTaskID returns a UUID5 derived deterministically from the Go version
// string (e.g. "go1.27.0").
//
// Uniqueness set rationale (per [[Watcher Writing Guide]] § Deterministic task_identifier):
//   - Same version → same task_id → controller dedup makes re-emit a no-op.
//   - A newer version → new name → new task_id → fresh task.
func DeriveTaskID(version string) uuid.UUID {
	return uuid.NewSHA1(taskIDNamespace, []byte("go-version:"+version))
}

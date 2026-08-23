// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"os"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// DefaultCursorPath is the default cursor persistence location.
// k8s mounts /data as a PVC; main.go binds CURSOR_PATH=DefaultCursorPath.
// A single configuration default, not an enum-set member — see the Default*
// rationale in pkg/sentryclient.go.
const DefaultCursorPath = "/data/cursor.json"

// Cursor is the per-short-id dedup state: the last UTC date (2006-01-02) a task
// was emitted for each Sentry short-id. A nil/empty Seen map means cold start
// (no prior run).
//
// Concurrency: not safe for concurrent use. The Watcher loads at poll start and
// saves at poll end (single goroutine).
type Cursor struct {
	Seen map[string]string `json:"seen"`
}

//counterfeiter:generate -o ../mocks/cursor_store.go --fake-name CursorStore . CursorStore

// CursorStore persists the watcher's cursor state between polls. The Watcher
// depends on this interface (not on package-level LoadCursor/SaveCursor) so its
// Poll logic is testable without real filesystem I/O
// (go-composition/no-package-function-calls-in-business-logic).
type CursorStore interface {
	Load(ctx context.Context) (*Cursor, error)
	Save(ctx context.Context, c *Cursor) error
}

// NewFileCursorStore returns the production CursorStore backed by the cursor
// file at path (typically CURSOR_PATH /data/cursor.json on the PVC mount).
func NewFileCursorStore(path string) CursorStore {
	return &fileCursorStore{path: path}
}

type fileCursorStore struct {
	path string
}

func (s *fileCursorStore) Load(ctx context.Context) (*Cursor, error) {
	return LoadCursor(ctx, s.path)
}

func (s *fileCursorStore) Save(ctx context.Context, c *Cursor) error {
	return SaveCursor(ctx, s.path, c)
}

// LoadCursor reads cursor state from path.
// Missing file → fresh empty cursor (cold start is valid).
// Corrupt file → error (caller should refuse to advance).
func LoadCursor(ctx context.Context, path string) (*Cursor, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is config-controlled
	if err != nil {
		if os.IsNotExist(err) {
			glog.V(2).Infof("cursor file not found, cold-start path=%s", path)
			return &Cursor{Seen: map[string]string{}}, nil
		}
		return nil, errors.Wrapf(ctx, err, "read cursor file path=%s", path)
	}
	var c Cursor
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, errors.Wrapf(ctx, err, "unmarshal cursor file path=%s", path)
	}
	if c.Seen == nil {
		c.Seen = map[string]string{}
	}
	return &c, nil
}

// SaveCursor persists cursor state to path atomically via temp file + rename.
func SaveCursor(ctx context.Context, path string, c *Cursor) error {
	data, err := json.Marshal(c)
	if err != nil {
		return errors.Wrapf(ctx, err, "marshal cursor state path=%s", path)
	}
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil { // #nosec G306 -- intentional 0600
		return errors.Wrapf(ctx, err, "write cursor tmp path=%s", path)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		return errors.Wrapf(ctx, err, "rename cursor tmp path=%s", path)
	}
	return nil
}

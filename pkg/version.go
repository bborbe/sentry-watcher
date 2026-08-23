// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"fmt"
	"regexp"
	"strconv"

	"github.com/bborbe/errors"
)

// versionPattern validates a Go release version string: "go" followed by
// major.minor with an optional patch (e.g. go1.27, go1.26.5).
var versionPattern = regexp.MustCompile(`^go(\d+)\.(\d+)(?:\.(\d+))?$`)

// Version is a parsed Go release version. Patch defaults to 0 when the source
// string omits it (e.g. "go1.27" → patch 0). Raw preserves the original string.
type Version struct {
	Major int
	Minor int
	Patch int
	Raw   string
}

// ParseVersion parses a Go release version string of the form
// go<major>.<minor>[.<patch>]. A missing patch component defaults to 0.
// Returns an error if the string does not match the expected shape.
func ParseVersion(ctx context.Context, s string) (Version, error) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil {
		return Version{}, errors.Errorf(ctx, "invalid go version %q", s)
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return Version{}, errors.Wrapf(ctx, err, "parse major of %q", s)
	}
	minor, err := strconv.Atoi(m[2])
	if err != nil {
		return Version{}, errors.Wrapf(ctx, err, "parse minor of %q", s)
	}
	patch := 0
	if m[3] != "" {
		patch, err = strconv.Atoi(m[3])
		if err != nil {
			return Version{}, errors.Wrapf(ctx, err, "parse patch of %q", s)
		}
	}
	return Version{Major: major, Minor: minor, Patch: patch, Raw: s}, nil
}

// Compare orders two versions by (major, minor, patch). It returns a negative
// number when v < other, zero when equal, and a positive number when v > other.
func (v Version) Compare(other Version) int {
	if v.Major != other.Major {
		return v.Major - other.Major
	}
	if v.Minor != other.Minor {
		return v.Minor - other.Minor
	}
	return v.Patch - other.Patch
}

// Less reports whether v sorts before other by (major, minor, patch).
func (v Version) Less(other Version) bool {
	return v.Compare(other) < 0
}

// String returns the canonical "go<major>.<minor>.<patch>" form. It is derived
// from the parsed components, not Raw, so it always includes the patch.
func (v Version) String() string {
	return fmt.Sprintf("go%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// Number returns the numeric "<major>.<minor>.<patch>" form without the "go"
// prefix, used in the human-readable task title.
func (v Version) Number() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

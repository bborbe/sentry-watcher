// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package handler provides the libhttp.WithError HTTP handlers for the
// sentry-watcher admin surface: cursor reset/set and poll trigger. Handlers
// live here — not as inline closures in main.go — per
// go-http-handler/no-inline-error-handler, so each is testable in isolation.
package handler

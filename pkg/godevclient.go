// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// DefaultGoDevURL is the go.dev release-list endpoint returning the JSON array
// of releases (including unstable ones — the client filters to stable).
const DefaultGoDevURL = "https://go.dev/dl/?mode=json"

//counterfeiter:generate -o ../mocks/go_dev_client.go --fake-name GoDevClient . GoDevClient

// GoDevClient is the upstream-source surface for the go-version watcher.
type GoDevClient interface {
	// LatestStable returns the maximum stable Go version reported by go.dev.
	// It returns an error when the request fails, the response is malformed, or
	// no stable, parseable version is present.
	LatestStable(ctx context.Context) (Version, error)
}

// goDevRelease is the subset of the go.dev release JSON the watcher consumes.
type goDevRelease struct {
	Version string `json:"version"`
	Stable  bool   `json:"stable"`
}

// NewGoDevClient returns the production GoDevClient backed by the given HTTP
// client and URL (typically DefaultGoDevURL).
func NewGoDevClient(httpClient *http.Client, url string) GoDevClient {
	return &goDevClient{httpClient: httpClient, url: url}
}

type goDevClient struct {
	httpClient *http.Client
	url        string
}

func (c *goDevClient) LatestStable(ctx context.Context) (Version, error) {
	releases, err := c.fetch(ctx)
	if err != nil {
		return Version{}, err
	}
	return maxStable(ctx, releases)
}

func (c *goDevClient) fetch(ctx context.Context) ([]goDevRelease, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "create request %s", c.url)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "get %s", c.url)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			glog.Warningf("close go.dev response body: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf(ctx, "go.dev returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, errors.Wrapf(ctx, err, "read go.dev response body")
	}
	var releases []goDevRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return nil, errors.Wrapf(ctx, err, "unmarshal go.dev response")
	}
	return releases, nil
}

// maxStable returns the maximum version among stable entries, skipping any
// entry that is not stable or does not parse as a Go version.
func maxStable(ctx context.Context, releases []goDevRelease) (Version, error) {
	var best Version
	found := false
	for _, r := range releases {
		if !r.Stable {
			continue
		}
		v, err := ParseVersion(ctx, r.Version)
		if err != nil {
			glog.V(2).Infof("skip unparseable go.dev version %q: %v", r.Version, err)
			continue
		}
		if !found || best.Less(v) {
			best = v
			found = true
		}
	}
	if !found {
		return Version{}, errors.Errorf(ctx, "no stable go version found in go.dev response")
	}
	return best, nil
}

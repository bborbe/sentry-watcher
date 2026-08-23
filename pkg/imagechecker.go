// Copyright (c) 2026 Benjamin Borbe All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package pkg

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/bborbe/errors"
	"github.com/golang/glog"
)

// DefaultDockerHubTokenURL is Docker Hub's token endpoint used to obtain a
// registry pull token before manifest requests.
const DefaultDockerHubTokenURL = "https://auth.docker.io/token" // #nosec G101 -- public Docker Hub auth endpoint, not a credential

// DefaultDockerHubRegistryURL is the Docker Hub registry v2 API base for the
// golang library image. Manifest path is <registry>/manifests/<tag>.
const DefaultDockerHubRegistryURL = "https://registry-1.docker.io/v2/library/golang"

// dockerHubTokenResponse is the subset of the Docker Hub token JSON consumed.
type dockerHubTokenResponse struct {
	Token string `json:"token"`
}

//counterfeiter:generate -o ../mocks/image_checker.go --fake-name ImageChecker . ImageChecker

// ImageChecker reports whether a golang docker image exists on Docker Hub.
// Consulted by the watcher before emitting a task for a new Go version: go.dev
// lists a release before docker.io publishes its image (12h–1day lag), so the
// gate holds the cursor until the image actually exists (see the goal's
// architecture decision on the Docker-image availability gate).
type ImageChecker interface {
	// ImageExists reports whether docker.io/library/golang:<version.Number()>
	// exists. Returns (false, nil) when the manifest is not yet published (404),
	// and an error on transport / auth / unexpected-status failures.
	ImageExists(ctx context.Context, version Version) (bool, error)
}

// NewImageChecker returns the production ImageChecker backed by the given HTTP
// client and Docker Hub endpoints (typically DefaultDockerHubTokenURL +
// DefaultDockerHubRegistryURL).
func NewImageChecker(httpClient *http.Client, tokenURL, registryURL string) ImageChecker {
	return &imageChecker{httpClient: httpClient, tokenURL: tokenURL, registryURL: registryURL}
}

type imageChecker struct {
	httpClient  *http.Client
	tokenURL    string
	registryURL string
}

// ImageExists implements ImageChecker. It obtains a pull token for the golang
// library image, then requests the manifest list for the version's numeric tag.
// 404 = not yet published; 200 = available; any other status or transport error
// is returned as an error (fail-closed: the watcher holds the cursor and retries).
func (c *imageChecker) ImageExists(ctx context.Context, version Version) (bool, error) {
	token, err := c.fetchToken(ctx)
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.registryURL+"/manifests/"+version.Number(),
		nil,
	)
	if err != nil {
		return false, errors.Wrapf(ctx, err, "create manifest request for %s", version)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(
		"Accept",
		"application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.index.v1+json",
	)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false, errors.Wrapf(ctx, err, "get manifest for %s", version)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			glog.Warningf("close manifest response body: %v", cerr)
		}
	}()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, errors.Errorf(
			ctx,
			"docker hub manifest status %d for %s",
			resp.StatusCode,
			version,
		)
	}
}

// fetchToken obtains a Docker Hub pull token for repository:library/golang.
func (c *imageChecker) fetchToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.tokenURL, nil)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "create token request %s", c.tokenURL)
	}
	q := req.URL.Query()
	q.Set("service", "registry.docker.io")
	q.Set("scope", "repository:library/golang:pull")
	req.URL.RawQuery = q.Encode()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", errors.Wrapf(ctx, err, "get docker hub token %s", c.tokenURL)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			glog.Warningf("close token response body: %v", cerr)
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return "", errors.Errorf(ctx, "docker hub token status %d", resp.StatusCode)
	}
	var tr dockerHubTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", errors.Wrapf(ctx, err, "decode docker hub token response")
	}
	if tr.Token == "" {
		return "", errors.Errorf(ctx, "docker hub token response missing token")
	}
	return tr.Token, nil
}

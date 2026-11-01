/*
   Copyright 2026 Docker Compose CLI authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/distribution/reference"
	clitypes "github.com/docker/cli/cli/config/types"
	"github.com/opencontainers/go-digest"

	"github.com/docker/compose/v5/internal/registry"
)

// maxManifestSize bounds the manifest we are willing to hash; manifests and
// token responses are tiny.
const maxManifestSize = 4 << 20

var registryHTTPClient = &http.Client{}

// registryRemoteDigest resolves the registry (multi-platform index) digest for
// an image reference. Podman's compat API does not implement Docker's
// /distribution endpoint, so compose must ask the registry itself.
func registryRemoteDigest(ctx context.Context, named reference.Named, credentials registry.AuthProvider) (digest.Digest, error) {
	if canonical, ok := named.(reference.Canonical); ok {
		return canonical.Digest(), nil
	}
	tag := "latest"
	if tagged, ok := reference.TagNameOnly(named).(reference.Tagged); ok {
		tag = tagged.Tag()
	}
	host := reference.Domain(named)
	if host == registry.DefaultNamespace {
		host = registry.DefaultRegistryHost
	}
	repo := reference.Path(named)
	manifestURL := fmt.Sprintf("https://%s/v2/%s/manifests/%s", host, repo, tag)

	auth, err := credentials.GetAuthConfig(reference.Domain(named))
	if err != nil {
		return "", err
	}
	resp, err := doManifestRequest(ctx, manifestURL, auth)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		token, err := requestToken(ctx, resp.Header.Get("Www-Authenticate"), auth, repo)
		_ = resp.Body.Close()
		if err != nil {
			return "", err
		}
		resp, err = doManifestRequest(ctx, manifestURL, clitypes.AuthConfig{RegistryToken: token})
		if err != nil {
			return "", err
		}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry %s returned %s for %s", host, resp.Status, named)
	}
	manifest, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize))
	if err != nil {
		return "", err
	}
	return digest.FromBytes(manifest), nil
}

func doManifestRequest(ctx context.Context, manifestURL string, auth clitypes.AuthConfig) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, http.NoBody)
	if err != nil {
		return nil, err
	}
	for _, mediaType := range []string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	} {
		req.Header.Add("Accept", mediaType)
	}
	if auth.RegistryToken != "" {
		req.Header.Set("Authorization", "Bearer "+auth.RegistryToken)
	}
	return registryHTTPClient.Do(req)
}

func requestToken(ctx context.Context, challenge string, auth clitypes.AuthConfig, repo string) (string, error) {
	params, ok := bearerChallenge(challenge)
	if !ok {
		return "", errors.New("registry did not return a Bearer challenge")
	}
	realm := params["realm"]
	if realm == "" {
		return "", errors.New("registry Bearer challenge has no realm")
	}
	query := url.Values{}
	if service := params["service"]; service != "" {
		query.Set("service", service)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	query.Set("scope", scope)
	separator := "?"
	if strings.Contains(realm, "?") {
		separator = "&"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm+separator+query.Encode(), http.NoBody)
	if err != nil {
		return "", err
	}
	if auth.Username != "" || auth.Password != "" {
		req.SetBasicAuth(auth.Username, auth.Password)
	}
	resp, err := registryHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token service returned %s", resp.Status)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxManifestSize)).Decode(&body); err != nil {
		return "", err
	}
	if body.Token != "" {
		return body.Token, nil
	}
	if body.AccessToken == "" {
		return "", errors.New("token service returned no token")
	}
	return body.AccessToken, nil
}

// bearerChallenge parses the params of a WWW-Authenticate Bearer header.
func bearerChallenge(header string) (map[string]string, bool) {
	const prefix = "bearer "
	if len(header) < len(prefix) || strings.ToLower(header[:len(prefix)]) != prefix {
		return nil, false
	}
	params := map[string]string{}
	for _, part := range strings.Split(header[len(prefix):], ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		params[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return params, true
}

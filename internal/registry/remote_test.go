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

package registry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/distribution/reference"
	clitypes "github.com/docker/cli/cli/config/types"
	"github.com/opencontainers/go-digest"
	"gotest.tools/v3/assert"
)

func TestRegistryRemoteDigestBearerChallenge(t *testing.T) {
	manifest := []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`)
	var tokenRequests, manifestRequests int

	mux := http.NewServeMux()
	server := httptest.NewTLSServer(mux)
	t.Cleanup(server.Close)

	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		tokenRequests++
		_, _ = w.Write([]byte(`{"token":"test-token"}`))
	})
	mux.HandleFunc("/v2/test/image/manifests/v1", func(w http.ResponseWriter, r *http.Request) {
		manifestRequests++
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.Header().Set("Www-Authenticate", fmt.Sprintf(`Bearer realm="https://%s/token",service="test",scope="repository:test/image:pull"`, r.Host))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(manifest)
	})

	previous := httpClient
	httpClient = server.Client()
	t.Cleanup(func() { httpClient = previous })

	named, err := reference.ParseNormalizedNamed(server.Listener.Addr().String() + "/test/image:v1")
	assert.NilError(t, err)
	got, err := RemoteDigest(t.Context(), named, AuthProviderFunc(func(string) (clitypes.AuthConfig, error) {
		return clitypes.AuthConfig{}, nil
	}))
	assert.NilError(t, err)
	assert.Equal(t, got.String(), digest.FromBytes(manifest).String())
	assert.Equal(t, manifestRequests, 2)
	assert.Equal(t, tokenRequests, 1)
}

func TestRegistryRemoteDigestCanonicalReference(t *testing.T) {
	want := "sha256:" + strings.Repeat("a", 64)
	named, err := reference.ParseNormalizedNamed("registry.example.com/team/app@" + want)
	assert.NilError(t, err)
	got, err := RemoteDigest(t.Context(), named, AuthProviderFunc(func(string) (clitypes.AuthConfig, error) {
		t.Fatal("credentials must not be fetched for a canonical reference")
		return clitypes.AuthConfig{}, nil
	}))
	assert.NilError(t, err)
	assert.Equal(t, got.String(), want)
}

func TestBearerChallenge(t *testing.T) {
	params, ok := bearerChallenge(`Bearer realm="https://auth.example.com/token",service="registry.example.com",scope="repository:team/app:pull"`)
	assert.Assert(t, ok)
	assert.Equal(t, params["realm"], "https://auth.example.com/token")
	assert.Equal(t, params["service"], "registry.example.com")
	assert.Equal(t, params["scope"], "repository:team/app:pull")

	_, ok = bearerChallenge(`Basic realm="registry"`)
	assert.Assert(t, !ok)
}

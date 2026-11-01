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
	"archive/tar"
	"os"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"gotest.tools/v3/assert"
)

func resetPodmanState(t *testing.T) {
	t.Helper()
	previous := rootfulPodmanSocket
	podmanProfile = false
	t.Cleanup(func() {
		podmanProfile = false
		rootfulPodmanSocket = previous
	})
}

func createPodmanSocket(t *testing.T, path string) {
	t.Helper()
	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	assert.NilError(t, os.WriteFile(path, nil, 0o600))
}

func TestConfigurePodmanHostExplicitPodman(t *testing.T) {
	resetPodmanState(t)
	dir := t.TempDir()
	socket := filepath.Join(dir, "podman", "podman.sock")
	createPodmanSocket(t, socket)
	t.Setenv(containerEngineEnv, "podman")
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")

	assert.NilError(t, configurePodmanHost("docker-compose", nil))
	assert.Equal(t, os.Getenv("DOCKER_HOST"), "unix://"+socket)
	assert.Assert(t, isPodman())
}

func TestConfigurePodmanHostExplicitPodmanWithoutSocket(t *testing.T) {
	resetPodmanState(t)
	rootfulPodmanSocket = filepath.Join(t.TempDir(), "missing.sock")
	t.Setenv(containerEngineEnv, "podman")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")

	err := configurePodmanHost("docker-compose", nil)
	assert.ErrorContains(t, err, "no Podman socket found")
	assert.Assert(t, !isPodman())
}

func TestConfigurePodmanHostByBinaryName(t *testing.T) {
	for _, tc := range []struct {
		argv0    string
		wantHost bool
	}{
		{argv0: "podman-compose", wantHost: true},
		{argv0: "docker-compose", wantHost: false},
	} {
		t.Run(tc.argv0, func(t *testing.T) {
			resetPodmanState(t)
			dir := t.TempDir()
			socket := filepath.Join(dir, "podman", "podman.sock")
			createPodmanSocket(t, socket)
			t.Setenv(containerEngineEnv, "")
			t.Setenv("XDG_RUNTIME_DIR", dir)
			t.Setenv("DOCKER_HOST", "")
			t.Setenv("DOCKER_CONTEXT", "")

			assert.NilError(t, configurePodmanHost(tc.argv0, nil))
			if tc.wantHost {
				assert.Equal(t, os.Getenv("DOCKER_HOST"), "unix://"+socket)
			} else {
				assert.Equal(t, os.Getenv("DOCKER_HOST"), "")
			}
		})
	}
}

func TestConfigurePodmanHostRespectsExplicitHost(t *testing.T) {
	resetPodmanState(t)
	t.Setenv(containerEngineEnv, "podman")
	t.Setenv("DOCKER_HOST", "unix:///custom/docker.sock")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	assert.NilError(t, configurePodmanHost("podman-compose", nil))
	assert.Equal(t, os.Getenv("DOCKER_HOST"), "unix:///custom/docker.sock")
}

func TestConfigurePodmanHostRespectsHostFlag(t *testing.T) {
	resetPodmanState(t)
	t.Setenv(containerEngineEnv, "podman")
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	assert.NilError(t, configurePodmanHost("podman-compose", []string{"--host", "tcp://127.0.0.1:2375"}))
	assert.Equal(t, os.Getenv("DOCKER_HOST"), "")
}

func TestIsPodmanFromDaemonHost(t *testing.T) {
	resetPodmanState(t)
	t.Setenv("DOCKER_HOST", "unix:///run/user/1000/podman/podman.sock")
	assert.Assert(t, isPodman())

	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	assert.Assert(t, !isPodman())
}

func TestPodmanHostPort(t *testing.T) {
	for _, tc := range []struct {
		published string
		number    int
		want      string
	}{
		{published: "8080", number: 3, want: "8080"},
		{published: "6005-6015", number: 1, want: "6005"},
		{published: "6005-6015", number: 5, want: "6009"},
	} {
		got, err := podmanHostPort(tc.published, tc.number)
		assert.NilError(t, err)
		assert.Equal(t, got, tc.want)
	}
	_, err := podmanHostPort("6005-6015", 12)
	assert.ErrorContains(t, err, "too small")
}

func TestCreateTarUsesRelativeTarget(t *testing.T) {
	b, err := createTar("hello", types.FileReferenceConfig{Target: "/run/secrets/bar"})
	assert.NilError(t, err)
	header, err := tar.NewReader(&b).Next()
	assert.NilError(t, err)
	assert.Equal(t, header.Name, "run/secrets/bar")
}

func TestPodmanFileReferenceFile(t *testing.T) {
	path, err := podmanFileReferenceFile("project", secretMount, "token", "s3cr3t")
	assert.NilError(t, err)
	content, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, string(content), "s3cr3t")
	info, err := os.Stat(path)
	assert.NilError(t, err)
	assert.Equal(t, info.Mode().Perm(), os.FileMode(0o644))
}

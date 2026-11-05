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
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"go.uber.org/mock/gomock"
	"gotest.tools/v3/assert"

	"github.com/docker/compose/v5/internal/engine"
)

func resetPodmanState(t *testing.T) {
	t.Helper()
	previous := rootfulPodmanSocket
	engine.SetPodmanProfile(false)
	t.Cleanup(func() {
		engine.SetPodmanProfile(false)
		rootfulPodmanSocket = previous
	})
}

func createPodmanSocket(t *testing.T, path string) {
	t.Helper()
	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	listener, err := net.Listen("unix", path)
	assert.NilError(t, err)
	t.Cleanup(func() {
		_ = listener.Close()
	})
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
	assert.ErrorContains(t, err, "rootless")
	assert.Assert(t, !isPodman())
}

func TestConfigurePodmanHostSkipsStaleSocket(t *testing.T) {
	resetPodmanState(t)
	dir := t.TempDir()
	stale := filepath.Join(dir, "podman", "podman.sock")
	assert.NilError(t, os.MkdirAll(filepath.Dir(stale), 0o755))
	assert.NilError(t, os.WriteFile(stale, nil, 0o600))
	rootful := filepath.Join(dir, "rootful.sock")
	createPodmanSocket(t, rootful)
	rootfulPodmanSocket = rootful
	t.Setenv(containerEngineEnv, "podman")
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")

	assert.NilError(t, configurePodmanHost("docker-compose", nil))
	assert.Equal(t, os.Getenv("DOCKER_HOST"), "unix://"+rootful)
}

func TestConfigurePodmanHostStaleSocketOnly(t *testing.T) {
	resetPodmanState(t)
	dir := t.TempDir()
	stale := filepath.Join(dir, "podman", "podman.sock")
	assert.NilError(t, os.MkdirAll(filepath.Dir(stale), 0o755))
	assert.NilError(t, os.WriteFile(stale, nil, 0o600))
	rootfulPodmanSocket = filepath.Join(dir, "missing.sock")
	t.Setenv(containerEngineEnv, "podman")
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("DOCKER_CONTEXT", "")

	err := configurePodmanHost("docker-compose", nil)
	assert.ErrorContains(t, err, "cannot connect to Podman socket")
	assert.ErrorContains(t, err, "rootless")
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

func TestPodmanSocketHostPath(t *testing.T) {
	assert.Equal(t, podmanSocketHostPath("unix:///run/user/1000/podman/podman.sock"), "/run/user/1000/podman/podman.sock")
	assert.Equal(t, podmanSocketHostPath("tcp://127.0.0.1:2375"), "")
	assert.Equal(t, podmanSocketHostPath("not a url"), "")
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

func TestCreateTarIncludesParentsAndOwnership(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix:///run/podman/podman.sock")
	mode := types.FileMode(0o440)
	b, err := createTar("hello", types.FileReferenceConfig{
		Target: "/run/secrets/bar",
		Mode:   &mode,
		UID:    "1005",
		GID:    "1005",
	})
	assert.NilError(t, err)

	var names []string
	var file *tar.Header
	reader := tar.NewReader(&b)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		assert.NilError(t, err)
		names = append(names, header.Name)
		if header.Name == "run/secrets/bar" {
			file = header
		}
	}
	assert.DeepEqual(t, names, []string{"run/", "run/secrets/", "run/secrets/bar"})
	assert.Assert(t, file != nil)
	assert.Equal(t, file.Uid, 1005)
	assert.Equal(t, file.Gid, 1005)
	assert.Equal(t, file.Mode, int64(0o440))
}

func TestPodmanInjectsSecretsAtCreateOnlyForAttachedUp(t *testing.T) {
	for _, tc := range []struct {
		name           string
		injectAtCreate bool
	}{
		{name: "create and detached up", injectAtCreate: false},
		{name: "interactive up", injectAtCreate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DOCKER_HOST", "unix:///run/user/1000/podman/podman.sock")
			svc, apiClient, _ := newStartPhaseTestService(t)

			service := types.ServiceConfig{
				Name: "web",
				ContainerSpec: types.ContainerSpec{
					Image:   "alpine",
					Secrets: []types.ServiceSecretConfig{{Source: "token"}},
				},
			}
			project := &types.Project{
				Name:     "test",
				Services: types.Services{"web": service},
				Secrets:  types.Secrets{"token": types.SecretConfig{Name: "token", Content: "s3cret"}},
			}

			apiClient.EXPECT().ContainerCreate(gomock.Any(), gomock.Any()).
				Return(client.ContainerCreateResult{ID: "web-id"}, nil)
			apiClient.EXPECT().ContainerInspect(gomock.Any(), "web-id", gomock.Any()).
				Return(client.ContainerInspectResult{Container: container.InspectResponse{
					ID:              "web-id",
					Name:            "/test-web-1",
					Config:          &container.Config{},
					NetworkSettings: &container.NetworkSettings{},
				}}, nil)
			if tc.injectAtCreate {
				apiClient.EXPECT().CopyToContainer(gomock.Any(), "web-id", gomock.Any()).
					Return(client.CopyToContainerResult{}, nil)
			}

			plan := &Plan{}
			plan.addNode(Operation{
				Type:       OpCreateContainer,
				ResourceID: "service:web:1",
				Cause:      "no existing container",
				Service:    &service,
				Name:       "test-web-1",
				Number:     1,
			}, "")

			exec := svc.newPlanExecutor(project, emptyObservedState("test"))
			exec.injectAtCreate = tc.injectAtCreate
			assert.NilError(t, exec.run(t.Context(), plan))
		})
	}
}

func TestPodmanLinkAliases(t *testing.T) {
	project := &types.Project{
		Name: "test",
		Services: types.Services{
			"web": {
				Name:  "web",
				Links: []string{"db:database", "cache"},
			},
			"db": {Name: "db"},
			"cache": {
				Name: "cache",
				ContainerSpec: types.ContainerSpec{
					Networks: map[string]*types.ServiceNetworkConfig{"back": nil},
				},
			},
		},
	}
	assert.DeepEqual(t, podmanLinkAliases(project, project.Services["db"], "default"), []string{"database"})
	assert.DeepEqual(t, podmanLinkAliases(project, project.Services["cache"], "back"), []string(nil))
	assert.DeepEqual(t, podmanLinkAliases(project, project.Services["cache"], "default"), []string(nil))
}

func TestPodmanFileReferenceVolume(t *testing.T) {
	service := types.ServiceConfig{ContainerSpec: types.ContainerSpec{
		Secrets: []types.ServiceSecretConfig{{Source: "token"}},
	}}
	mounts := podmanFileReferenceVolume(service, nil)
	assert.Equal(t, len(mounts), 1)
	assert.Equal(t, string(mounts[0].Type), "volume")
	assert.Equal(t, mounts[0].Target, "/run/secrets")

	absolute := types.ServiceConfig{ContainerSpec: types.ContainerSpec{
		Secrets: []types.ServiceSecretConfig{{Source: "token", Target: "/etc/token"}},
	}}
	assert.Equal(t, len(podmanFileReferenceVolume(absolute, nil)), 0)

	// A config injected under /run/secrets (use_api_socket) needs it too.
	config := types.ServiceConfig{ContainerSpec: types.ContainerSpec{
		Configs: []types.ServiceConfigObjConfig{{Source: "#apisocket", Target: "/run/secrets/docker/config.json"}},
	}}
	assert.Equal(t, len(podmanFileReferenceVolume(config, nil)), 1)

	// A config at its default location is not in the tmpfs.
	defaultConfig := types.ServiceConfig{ContainerSpec: types.ContainerSpec{
		Configs: []types.ServiceConfigObjConfig{{Source: "app.conf"}},
	}}
	assert.Equal(t, len(podmanFileReferenceVolume(defaultConfig, nil)), 0)

	// A user-provided mount at /run/secrets takes precedence.
	assert.Equal(t, len(podmanFileReferenceVolume(service, []mount.Mount{{Target: "/run/secrets"}})), 0)
}

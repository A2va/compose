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
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/cli"
)

// Podman support: compose talks to Podman through its Docker-compatible API
// (podman system service). See PODMAN.md at the repository root for the plan,
// the compatibility matrix and the overlay index.

const containerEngineEnv = "COMPOSE_CONTAINER_ENGINE"

// podmanProfile records that the engine was resolved to Podman at startup,
// before docker/cli initializes its client.
var podmanProfile bool

// podmanConfigFiles are accepted as default compose file names in Podman mode.
// They are appended after the standard names, so a project that has both keeps
// using compose.yaml.
var (
	podmanConfigFiles         = []string{"podman-compose.yml", "podman-compose.yaml"}
	podmanOverrideConfigFiles = []string{"podman-compose.override.yml", "podman-compose.override.yaml"}
)

// rootfulPodmanSocket is a variable so tests can isolate from the host.
var rootfulPodmanSocket = "/run/podman/podman.sock"

// podmanLookPath is a variable so tests can control whether Podman is installed.
var podmanLookPath = exec.LookPath

// podmanSocketProbeTimeout bounds the startup check. A systemd socket unit
// accepts the connection immediately and starts the service afterwards, so a
// short timeout is enough to tell a live socket from a stale file.
const podmanSocketProbeTimeout = time.Second

const podmanSocketHints = "for rootless Podman enable " +
	"'systemctl --user enable --now podman.socket', for rootful enable " +
	"'systemctl enable --now podman.socket', or run 'podman system service --time=0'"

// ConfigurePodmanHost points this process at the Podman socket when asked to.
// It must run before docker/cli initializes its client, i.e. before plugin.Run.
func ConfigurePodmanHost() error {
	return configurePodmanHost(filepath.Base(os.Args[0]), os.Args[1:])
}

func configurePodmanHost(argv0 string, args []string) error {
	switch os.Getenv(containerEngineEnv) {
	case "docker":
		return nil
	case "podman":
		enablePodman()
		return defaultPodmanHost(true, args)
	}
	// `podman compose` exports DOCKER_HOST pointing at the Podman socket, so
	// wrapper-driven runs are detected even when the provider is docker-compose.
	if isPodman() {
		enablePodman()
		return nil
	}
	// A podman-compose binary requires Podman. Every other name also prefers
	// Podman: with the podman binary installed a missing socket is an error,
	// while a host without Podman keeps Docker's default.
	if strings.Contains(argv0, "podman") {
		enablePodman()
		return defaultPodmanHost(true, args)
	}
	if engineHostConfigured(args) {
		return nil
	}
	if podmanInstalled() {
		enablePodman()
		return defaultPodmanHost(true, args)
	}
	return defaultPodmanHost(false, args)
}

func podmanInstalled() bool {
	_, err := podmanLookPath("podman")
	return err == nil
}

func enablePodman() {
	podmanProfile = true
	addPodmanConfigFiles()
}

func addPodmanConfigFiles() {
	for _, name := range podmanConfigFiles {
		if !slices.Contains(cli.DefaultFileNames, name) {
			cli.DefaultFileNames = append(cli.DefaultFileNames, name)
		}
	}
	for _, name := range podmanOverrideConfigFiles {
		if !slices.Contains(cli.DefaultOverrideFileNames, name) {
			cli.DefaultOverrideFileNames = append(cli.DefaultOverrideFileNames, name)
		}
	}
}

func defaultPodmanHost(required bool, args []string) error {
	if engineHostConfigured(args) {
		return nil
	}
	socket, err := podmanSocketPath(required)
	if err != nil {
		podmanProfile = false
		return err
	}
	if socket == "" {
		podmanProfile = false
		if required {
			return errors.New("no Podman socket found; " + podmanSocketHints)
		}
		return nil
	}
	enablePodman()
	return os.Setenv("DOCKER_HOST", "unix://"+socket)
}

func engineHostConfigured(args []string) bool {
	if os.Getenv("DOCKER_HOST") != "" || os.Getenv("DOCKER_CONTEXT") != "" {
		return true
	}
	for _, arg := range args {
		if arg == "-H" || arg == "--host" || strings.HasPrefix(arg, "--host=") ||
			arg == "--context" || strings.HasPrefix(arg, "--context=") {
			return true
		}
	}
	return false
}

// podmanSocketPath returns the first Podman socket that accepts a connection.
// The probe also triggers systemd socket activation. In strict mode a stale or
// inaccessible socket is an error rather than a silent fallback to Docker.
func podmanSocketPath(strict bool) (string, error) {
	var (
		failed string
		cause  error
	)
	for _, socket := range podmanSocketCandidates(os.Getenv("XDG_RUNTIME_DIR"), os.Geteuid() == 0) {
		ok, err := probePodmanSocket(socket)
		if ok {
			return socket, nil
		}
		if err != nil && failed == "" {
			failed, cause = socket, err
		}
	}
	if strict && failed != "" {
		return "", fmt.Errorf("cannot connect to Podman socket %s: %w; %s", failed, cause, podmanSocketHints)
	}
	return "", nil
}

// podmanSocketCandidates returns the sockets to probe in preference order. A
// root process (e.g. sudo podman-compose) prefers the rootful socket even when
// XDG_RUNTIME_DIR still points at the invoking user's runtime directory.
func podmanSocketCandidates(runtimeDir string, root bool) []string {
	if runtimeDir == "" {
		return []string{rootfulPodmanSocket}
	}
	rootless := filepath.Join(runtimeDir, "podman", "podman.sock")
	if root {
		return []string{rootfulPodmanSocket, rootless}
	}
	return []string{rootless, rootfulPodmanSocket}
}

func probePodmanSocket(path string) (bool, error) {
	conn, err := net.DialTimeout("unix", path, podmanSocketProbeTimeout)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	_ = conn.Close()
	return true, nil
}

// isPodman reports whether this invocation targets Podman. `podman compose`
// exports DOCKER_HOST pointing at the Podman socket, so the socket path also
// serves as detection for invocations that skip our startup defaults.
func isPodman() bool {
	return podmanProfile || strings.Contains(os.Getenv("DOCKER_HOST"), "podman")
}

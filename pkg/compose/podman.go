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
	"path/filepath"
	"strings"
	"time"

	"github.com/docker/compose/v5/internal/engine"
)

// Podman support: compose talks to Podman through its Docker-compatible API
// (podman system service). See PODMAN.md at the repository root for the plan,
// the compatibility matrix and the overlay index.

const containerEngineEnv = "COMPOSE_CONTAINER_ENGINE"

// rootfulPodmanSocket is a variable so tests can isolate from the host.
var rootfulPodmanSocket = "/run/podman/podman.sock"

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
		engine.SetPodmanProfile(true)
		return defaultPodmanHost(true, args)
	}
	// A podman-compose binary defaults to the Podman socket; docker-compose
	// keeps its Docker default even when a Podman socket is present.
	if !strings.Contains(argv0, "podman") {
		return nil
	}
	engine.SetPodmanProfile(true)
	return defaultPodmanHost(false, args)
}

func defaultPodmanHost(required bool, args []string) error {
	if engineHostConfigured(args) {
		return nil
	}
	socket, err := podmanSocketPath()
	if err != nil {
		engine.SetPodmanProfile(false)
		return err
	}
	if socket == "" {
		engine.SetPodmanProfile(false)
		if required {
			return errors.New("no Podman socket found; " + podmanSocketHints)
		}
		return nil
	}
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
// The probe also triggers systemd socket activation. A stale socket file is an
// error rather than a silent fallback to the Docker default.
func podmanSocketPath() (string, error) {
	var (
		failed string
		cause  error
	)
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		socket := filepath.Join(runtimeDir, "podman", "podman.sock")
		ok, err := probePodmanSocket(socket)
		if ok {
			return socket, nil
		}
		if err != nil {
			failed, cause = socket, err
		}
	}
	ok, err := probePodmanSocket(rootfulPodmanSocket)
	if ok {
		return rootfulPodmanSocket, nil
	}
	if err != nil && failed == "" {
		failed, cause = rootfulPodmanSocket, err
	}
	if failed != "" {
		return "", fmt.Errorf("cannot connect to Podman socket %s: %w; %s", failed, cause, podmanSocketHints)
	}
	return "", nil
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

func isPodman() bool {
	return engine.IsPodman()
}

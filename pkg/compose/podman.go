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
	"os"
	"path/filepath"
	"strings"
)

// Podman support: compose talks to Podman through its Docker-compatible API
// (podman system service). See PODMAN.md at the repository root for the plan,
// the compatibility matrix and the overlay index.

const containerEngineEnv = "COMPOSE_CONTAINER_ENGINE"

// podmanProfile records that the engine was resolved to Podman at startup,
// before docker/cli initializes its client.
var podmanProfile bool

// rootfulPodmanSocket is a variable so tests can isolate from the host.
var rootfulPodmanSocket = "/run/podman/podman.sock"

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
		podmanProfile = true
		return defaultPodmanHost(true, args)
	}
	// A podman-compose binary defaults to the Podman socket; docker-compose
	// keeps its Docker default even when a Podman socket is present.
	if !strings.Contains(argv0, "podman") {
		return nil
	}
	podmanProfile = true
	return defaultPodmanHost(false, args)
}

func defaultPodmanHost(required bool, args []string) error {
	if engineHostConfigured(args) {
		return nil
	}
	socket := podmanSocketPath()
	if socket == "" {
		podmanProfile = false
		if required {
			return errors.New("no Podman socket found; enable it with " +
				"'systemctl --user enable --now podman.socket' (rootless) " +
				"or 'systemctl enable --now podman.socket' (root)")
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

func podmanSocketPath() string {
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		rootless := filepath.Join(runtimeDir, "podman", "podman.sock")
		if _, err := os.Stat(rootless); err == nil {
			return rootless
		}
	}
	if _, err := os.Stat(rootfulPodmanSocket); err == nil {
		return rootfulPodmanSocket
	}
	return ""
}

// isPodman reports whether this invocation targets Podman. `podman compose`
// exports DOCKER_HOST pointing at the Podman socket, so the socket path also
// serves as detection for invocations that skip our startup defaults.
func isPodman() bool {
	return podmanProfile || strings.Contains(os.Getenv("DOCKER_HOST"), "podman")
}

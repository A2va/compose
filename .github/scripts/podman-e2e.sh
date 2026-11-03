#!/usr/bin/env bash
#
# Copyright 2026 Docker Compose CLI authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
# Runs the Podman e2e smoke subset inside a privileged container image.
# Invoked by .github/workflows/podman-e2e.yml; the matrix supplies IMAGE.
# The suite is built on the host (CGO disabled) and executed inside the
# container so the Podman version under test needs no host compatibility.
set -euo pipefail

if command -v apt-get >/dev/null 2>&1; then
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq podman podman-docker iptables curl
else
  dnf install -y -q podman podman-docker iptables curl
fi

mkdir -p /etc/containers/containers.conf.d /root/.docker
# No journald inside the CI container: force the file log driver.
printf '[containers]\nlog_driver = "k8s-file"\n' > /etc/containers/containers.conf.d/99-podman-e2e.conf
# overlay is much faster than vfs; fall back if nested overlay is unsupported.
driver="${STORAGE_DRIVER:-overlay}"
printf '[storage]\ndriver = "%s"\nrunroot = "/run/containers/storage"\ngraphroot = "/var/lib/containers/storage"\n' "$driver" > /etc/containers/storage.conf
if ! podman info >/dev/null 2>&1; then
  printf '[storage]\ndriver = "vfs"\nrunroot = "/run/containers/storage"\ngraphroot = "/var/lib/containers/storage"\n' > /etc/containers/storage.conf
fi
printf '{}\n' > /root/.docker/config.json

# Optional: authenticated pulls avoid Docker Hub's anonymous rate limit.
if [ -n "${DOCKERHUB_USERNAME:-}" ] && [ -n "${DOCKERHUB_TOKEN:-}" ]; then
  printf '%s' "$DOCKERHUB_TOKEN" | podman login -u "$DOCKERHUB_USERNAME" --password-stdin docker.io
fi

mkdir -p /run/podman
podman system service --time=0 >/tmp/podman-service.log 2>&1 &
for _ in $(seq 1 100); do
  [ -S /run/podman/podman.sock ] && break
  sleep 0.2
done
if [ ! -S /run/podman/podman.sock ]; then
  cat /tmp/podman-service.log
  exit 1
fi

export DOCKER_HOST=unix:///run/podman/podman.sock
export COMPOSE_E2E_BIN_PATH="${E2E_BIN:?E2E_BIN must point at the built compose binary}"

# buildah mounts an overlay for the build context; on Fedora 45 the container's
# overlay root rejects that, while a tmpfs works.
mount -t tmpfs tmpfs /var/tmp 2>/dev/null || true

# Classic-build smoke check (H2), independent of the suite.
workdir=$(mktemp -d)
printf 'FROM scratch\nCOPY hello /hello\n' > "$workdir/Dockerfile"
printf 'hi\n' > "$workdir/hello"
printf 'services:\n  app:\n    build: .\n    image: localhost/compose-podman-ci:test\n' > "$workdir/compose.yaml"
(cd "$workdir" && "$E2E_BIN" -p podman-ci build >/dev/null)

# TestLoggingDriver needs a real Docker CLI for its Desktop probe; the CI
# images only provide podman-docker, whose `docker info -f` lacks that field.
cd pkg/e2e
../../bin/e2e.test -test.v -test.count=1 -test.parallel 4 -test.timeout 25m \
  -test.run 'TestUpNoDeps$|TestPs$|TestCopy$|TestExec$|TestStartStop$|TestEventsUnknownService$|TestNetworks$|TestUpWait$|TestLocalComposeLogs$|TestPortRange$|TestSecretFromInclude$|TestComposePull$|TestNetworkAliases$|TestNetworkLinks$|TestSecretFromEnv$|TestLocalComposeExec$|TestLocalComposeExecOneOff$|TestPause$|TestPauseServiceAlreadyPaused$|TestConfigFromEnv$|TestUnsetEnv$|TestUpDryRunMissingImage$|TestWatchSyncIntoSymlinkedDirectory$|TestCascadeStop$|TestCascadeFail$|TestCascadeIgnoresOneOffContainer$|TestExposeRange$|TestInitContainer$|TestCompatibility$|TestConfig$|TestMacAddress$|TestNetworkModes$|TestPostStartHookSuccess$|TestPreStartHookSuccess$|TestPreStopHookSuccess$|TestRestart$|TestStartStopWithDependencies$|TestUpDependenciesNotStopped$'

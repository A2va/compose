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
# Boots the matrix image with systemd as PID 1 and runs the Podman e2e suite
# inside it. Podman schedules healthchecks through systemd transient timers, so
# a plain `docker run` container without systemd would leave every healthcheck
# in "starting". Invoked by both e2e workflows; IMAGE and E2E_BIN come from the
# environment, and the repository is bind-mounted at $PWD. The Docker CLI
# version is optional (the inner script pins one that supports Podman 4.3).
set -Eeuo pipefail

# This container is privileged, shares the host cgroup namespace and mounts
# /sys/fs/cgroup writable. Its systemd can start a getty on the host's tty1 and
# overwrite the local console session. CI runners are disposable; elsewhere the
# caller must opt in explicitly (and should use a disposable VM).
if [ -z "${CI:-}" ] && [ "${PODMAN_E2E_ALLOW_LOCAL:-}" != "1" ]; then
  echo "Refusing to run the privileged e2e container outside CI." >&2
  echo "Use a disposable VM, or set PODMAN_E2E_ALLOW_LOCAL=1 to accept the risk." >&2
  exit 1
fi

: "${IMAGE:?IMAGE must name the test container image}"
: "${E2E_BIN:?E2E_BIN must point at the built compose binary}"

# Ubuntu 26.04's pasta AppArmor profile rejects the SIGTERM rootless Podman
# sends to its network helper, so every teardown fails with "rootless netns:
# kill network process: permission denied". The container-level
# apparmor=unconfined does not stop the host profile attaching when the nested
# engine execs pasta. Apply the runner-image amendment until it ships.
pasta_profile=/etc/apparmor.d/usr.bin.pasta
if [ -f "$pasta_profile" ] && ! grep -q 'peer=podman' "$pasta_profile"; then
  as_root() {
    if [ "$(id -u)" -eq 0 ]; then "$@"; else sudo "$@"; fi
  }
  echo "Allowing rootless Podman to stop pasta under AppArmor" >&2
  as_root sed -i '$ i\  signal (receive) set=(term) peer=podman,' "$pasta_profile"
  as_root apparmor_parser -r "$pasta_profile"
fi

name="podman-e2e-$$"
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --name "$name" --privileged --cgroupns=host \
  --security-opt=label=disable \
  --security-opt=apparmor=unconfined \
  --tmpfs=/run --tmpfs=/run/lock --tmpfs=/tmp:rw,nosuid,exec \
  --tmpfs=/var/tmp:rw,exec \
  --stop-signal=SIGRTMIN+3 \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  -v "$PWD:$PWD" -w "$PWD" \
  -e E2E_BIN="$E2E_BIN" \
  -e E2E_ROOTLESS \
  -e STORAGE_DRIVER -e DOCKER_CLI_VERSION \
  -e DOCKERHUB_USERNAME -e DOCKERHUB_TOKEN \
  "$IMAGE" bash -c '
    if command -v apt-get >/dev/null 2>&1; then
      apt-get update -qq
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq systemd >/dev/null
    else
      dnf install -y -q systemd >/dev/null
    fi
    init=/sbin/init
    [ -x "$init" ] || init=/lib/systemd/systemd
    [ -x "$init" ] || init=/usr/lib/systemd/systemd
    exec "$init"
  '

state=unknown
for _ in $(seq 1 60); do
  state=$(docker exec "$name" systemctl is-system-running 2>/dev/null || true)
  case "$state" in
    running | degraded) break ;;
  esac
  sleep 1
done
if [ "$state" != "running" ] && [ "$state" != "degraded" ]; then
  echo "systemd did not start in $IMAGE (state: $state)" >&2
  docker logs "$name" >&2 || true
  exit 1
fi

docker exec \
  -e E2E_BIN="$E2E_BIN" \
  -e E2E_ROOTLESS \
  -e STORAGE_DRIVER -e DOCKER_CLI_VERSION \
  -e DOCKERHUB_USERNAME -e DOCKERHUB_TOKEN \
  -w "$PWD" "$name" bash "$PWD/.github/scripts/podman-e2e.sh"

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
# Runs the Podman e2e suite inside a privileged container image, skipping the
# gaps documented in PODMAN.md.
# Invoked by the e2e workflows; the matrix supplies IMAGE. The suite is built on
# the host (CGO disabled) and executed inside the container so the Podman
# version under test needs no host compatibility.
#
# E2E_ROOTLESS=1 runs the suite as a non-root user, matching the rootless host
# baseline. E2E_ROOTLESS=0 runs it as root. Both modes use the same denylist,
# so the commit/job-only/idempotent-up/scale/interactive-hook cases run in
# both and a failure is a real engine gap rather than a reason to drop a mode.
set -euo pipefail

e2e_rootless="${E2E_ROOTLESS:-0}"
: "${E2E_BIN:?E2E_BIN must point at the built compose binary}"
E2E_BIN="$(readlink -f "$E2E_BIN")"

if command -v apt-get >/dev/null 2>&1; then
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq podman iptables curl util-linux uidmap slirp4netns fuse-overlayfs
  # Podman 4.3 (Debian 12) uses CNI; newer releases use netavark. Install both
  # when the distribution packages them, but do not fail on the other.
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq containernetworking-plugins || true
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq netavark || true
else
  dnf install -y -q podman iptables curl util-linux shadow-utils libcap slirp4netns fuse-overlayfs
  # Fedora 5.x uses pasta for rootless networking.
  dnf install -y -q passt || true
  # Fedora 45 ships newuidmap/newgidmap without setuid or file capabilities,
  # so rootless Podman cannot write its uid_map inside this container. File
  # capabilities (rather than setuid) are what works under the container's
  # no_new_privs-style restrictions.
  setcap cap_setuid,cap_setgid+ep /usr/bin/newuidmap
  setcap cap_setuid,cap_setgid+ep /usr/bin/newgidmap
fi

# The suite drives the `docker` CLI for auxiliary commands (inspect, info,
# context, top) exactly like a local run against Podman's socket. Install the
# real client instead of podman-docker's wrapper, whose reduced commands and
# output types break the CLI-probe tests. Docker 29 raised the minimum API to
# 1.44, above Podman 4.3's 1.40, so pin a client that still negotiates down.
docker_cli_version="${DOCKER_CLI_VERSION:-27.5.1}"
case "$(uname -m)" in
  x86_64 | amd64) docker_cli_arch=x86_64 ;;
  aarch64 | arm64) docker_cli_arch=aarch64 ;;
  *)
    echo "unsupported architecture: $(uname -m)" >&2
    exit 1
    ;;
esac
curl -fsSL "https://download.docker.com/linux/static/stable/${docker_cli_arch}/docker-${docker_cli_version}.tgz" |
  tar -xz -C /usr/local/bin --strip-components=1 docker/docker
docker_path="$(readlink -f "$(command -v docker)")"
if [ "$docker_path" != /usr/local/bin/docker ]; then
  echo "podman-docker or another wrapper shadows the real Docker CLI: $(command -v docker) (resolved: $docker_path)" >&2
  exit 1
fi
docker --version

mkdir -p /etc/containers/containers.conf.d /root/.docker
# No journald inside the CI container: force the file log driver.
printf '[containers]\nlog_driver = "k8s-file"\n' > /etc/containers/containers.conf.d/99-podman-e2e.conf
printf '{}\n' > /root/.docker/config.json

if [ "$e2e_rootless" = "1" ]; then
  # Run the suite as a non-root user whose uid matches the bind-mounted repo,
  # so tests that write into the checkout keep working. Container root in a
  # rootless engine maps to this user, which is also why the bridge transformer
  # can read its 0600 input (see pkg/bridge/convert.go).
  repo_uid="$(stat -c '%u' "$PWD")"
  [ "$repo_uid" -eq 0 ] && repo_uid=1000
  if id -u "$repo_uid" >/dev/null 2>&1; then
    e2e_user="$(id -nu "$repo_uid")"
  else
    e2e_user=e2e
    useradd -m -u "$repo_uid" -s /bin/bash "$e2e_user"
  fi
  e2e_uid="$(id -u "$e2e_user")"
  e2e_gid="$(id -g "$e2e_user")"
  e2e_home="$(getent passwd "$e2e_user" | cut -d: -f6)"
  e2e_runtime="/run/user/$e2e_uid"

  [ -d "$e2e_home" ] || install -d -o "$e2e_user" -g "$e2e_gid" "$e2e_home"
  grep -q "^${e2e_user}:" /etc/subuid 2>/dev/null || echo "${e2e_user}:100000:65536" >>/etc/subuid
  grep -q "^${e2e_user}:" /etc/subgid 2>/dev/null || echo "${e2e_user}:100000:65536" >>/etc/subgid

  install -d -o "$e2e_user" -g "$e2e_gid" -m 700 "$e2e_runtime"
  loginctl enable-linger "$e2e_user" 2>/dev/null || true
  for _ in $(seq 1 300); do
    [ -S "$e2e_runtime/bus" ] && break
    sleep 0.2
  done
  if [ ! -S "$e2e_runtime/bus" ]; then
    echo "systemd user manager did not start for $e2e_user" >&2
    exit 1
  fi

  export DOCKER_HOST="unix://$e2e_runtime/podman/podman.sock"
  export COMPOSE_E2E_BIN_PATH="${E2E_BIN:?E2E_BIN must point at the built compose binary}"

  run_e2e() {
    runuser -u "$e2e_user" -- env \
      HOME="$e2e_home" \
      USER="$e2e_user" \
      LOGNAME="$e2e_user" \
      XDG_RUNTIME_DIR="$e2e_runtime" \
      DOCKER_HOST="$DOCKER_HOST" \
      COMPOSE_E2E_BIN_PATH="$COMPOSE_E2E_BIN_PATH" \
      PATH="/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin" \
      "$@"
  }

  write_rootless_storage_conf() {
    local driver="$1"
    local mount_program
    mount_program="$(command -v fuse-overlayfs || true)"
    cat >"$e2e_home/.config/containers/storage.conf" <<EOF
[storage]
driver = "$driver"
runroot = "$e2e_runtime/containers"
graphroot = "$e2e_home/.local/share/containers/storage"
EOF
    if [ "$driver" = "overlay" ] && [ -n "$mount_program" ]; then
      printf '\n[storage.options.overlay]\nmount_program = "%s"\n' "$mount_program" >>"$e2e_home/.config/containers/storage.conf"
    fi
    # install -d created these parents as root; Podman rejects a root-owned $HOME/.config.
    chown -R "$e2e_user:$e2e_gid" "$e2e_home/.config" "$e2e_home/.local"
  }

  install -d -o "$e2e_user" -g "$e2e_gid" \
    "$e2e_home/.config/containers" "$e2e_home/.local/share/containers/storage" "$e2e_home/.docker"
  write_rootless_storage_conf "${STORAGE_DRIVER:-vfs}"
  printf '{}\n' >"$e2e_home/.docker/config.json"
  chown "$e2e_user:$e2e_gid" "$e2e_home/.docker/config.json"

  # Overlay is faster, but rootless overlay needs fuse-overlayfs and a kernel
  # that allows it; fall back to vfs rather than failing the whole leg.
  if ! run_e2e podman info >/tmp/podman-rootless-info.log 2>&1; then
    cat /tmp/podman-rootless-info.log
    write_rootless_storage_conf vfs
    run_e2e podman info >/dev/null
  fi

  if [ -n "${DOCKERHUB_USERNAME:-}" ] && [ -n "${DOCKERHUB_TOKEN:-}" ]; then
    printf '%s' "$DOCKERHUB_TOKEN" | run_e2e podman login -u "$DOCKERHUB_USERNAME" --password-stdin docker.io
  fi

  run_e2e mkdir -p "$e2e_runtime/podman"
  run_e2e podman system service --time=0 >/tmp/podman-service.log 2>&1 &
  for _ in $(seq 1 100); do
    [ -S "$e2e_runtime/podman/podman.sock" ] && break
    sleep 0.2
  done
  if [ ! -S "$e2e_runtime/podman/podman.sock" ]; then
    cat /tmp/podman-service.log
    exit 1
  fi
else
  # Rootful fallback. Keep the historical storage setup and socket path.
  driver="${STORAGE_DRIVER:-overlay}"
  printf '[storage]\ndriver = "%s"\nrunroot = "/run/containers/storage"\ngraphroot = "/var/lib/containers/storage"\n' "$driver" >/etc/containers/storage.conf
  if ! podman info >/dev/null 2>&1; then
    printf '[storage]\ndriver = "vfs"\nrunroot = "/run/containers/storage"\ngraphroot = "/var/lib/containers/storage"\n' >/etc/containers/storage.conf
  fi

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

  run_e2e() { "$@"; }
fi

podman_version="$(run_e2e podman --version | awk '{print $3}')"
podman_major="${podman_version%%.*}"
podman_minor="${podman_version#*.}"
podman_minor="${podman_minor%%.*}"

# Podman schedules healthchecks as systemd transient timers; verify the
# container actually has a working systemd before the suite relies on it, so a
# missing scheduler fails fast instead of hanging every healthcheck test.
run_e2e podman run -d --name podman-e2e-healthcheck \
  --health-cmd=true --health-interval=1s --health-retries=2 \
  alpine sleep 60 >/dev/null
health=unknown
for _ in $(seq 1 60); do
  health=$(run_e2e podman inspect -f '{{.State.Health.Status}}' podman-e2e-healthcheck 2>/dev/null || echo unknown)
  [ "$health" = "healthy" ] && break
  sleep 1
done
run_e2e podman rm -f podman-e2e-healthcheck >/dev/null 2>&1 || true
if [ "$health" != "healthy" ]; then
  echo "healthchecks are not scheduled (status: $health); the test container needs systemd" >&2
  exit 1
fi

# Classic-build smoke check (H2), independent of the suite.
workdir="$(run_e2e mktemp -d)"
printf 'FROM scratch\nCOPY hello /hello\n' >"$workdir/Dockerfile"
printf 'hi\n' >"$workdir/hello"
printf 'services:\n  app:\n    build: .\n    image: localhost/compose-podman-ci:test\n' >"$workdir/compose.yaml"
(cd "$workdir" && run_e2e "$E2E_BIN" -p podman-ci build >/dev/null)

# Denylist: every test runs unless named here. Keep each entry tied to the
# PODMAN.md section that documents the gap, so a test that starts passing (or a
# gap that gets fixed) is removed from this list together with its note.
skip_tests=(
  # Known Podman gaps: BuildKit/buildx/build log and error wording (buildah
  # classic builder). PODMAN.md, "Full-suite baseline" and "Known Podman gaps".
  TestBuildBuilder
  TestBuildDependentImageWithProfile
  TestBuildEntitlements
  TestBuildImageDependencies
  TestBuildPlatformsStandardErrors
  TestBuildPlatformsWithCorrectBuildxConfig
  TestBuildPrivileged
  TestBuildSecrets
  TestBuildSSH
  TestComposeRunBuild

  # Engine/watch behavior differences.
  TestIPAMConfig
  TestNoDepsNetworkMode
  TestUpAttachedTerminatesOnExternalStop
  TestRebuildOnDotEnvWithExternalNetwork
  TestWatchMultiServices

  # Anonymous-volume inspect metadata: Docker reports Mode "z" for a volume
  # created through the mount API, Podman reports it empty. PODMAN.md,
  # "Known Podman gaps".
  TestLocalComposeVolume

  # The real Docker CLI fixes the wrapper-only probes (containerd store, umask,
  # image inspect), but these still depend on engine behavior that differs on
  # the matrix. TestBuildTLS selects a Docker-in-Docker context while
  # DOCKER_HOST still points at Podman, so the Podman profile forces the
  # classic builder; TestLocalComposeUp reads the compose top layout;
  # TestLoggingDriver relies on the log-driver fallback.
  TestBuildTLS
  TestLocalComposeUp
  TestLoggingDriver

  # Environment-dependent failures: fixed test subnets, logs-follow cold
  # start and the restart/log-attach race.
  TestNetworkConfigChanged
  TestLocalComposeLogsFollow
  TestAttachRestart
)

# Version gaps are skipped in both modes, so the baseline stays identical for
# rootless and rootful on a given Podman version. Podman 4.3 and 4.9 ship a
# buildah that predates Dockerfile heredocs, so the escaped-build scenario's
# heredoc step cannot run there; current Fedora (6.x) resolves it.
if [ "${podman_major:-0}" -lt 5 ]; then
  skip_tests+=(
    TestBuildEscaped
  )
fi
if [ "${podman_major:-0}" -eq 4 ] && [ "${podman_minor:-0}" -le 3 ]; then
  skip_tests+=(
    TestCommit
    TestCommitWithReplicas
    # A rebuild can keep the previous image ID, so compose keeps the old
    # containers; current Podman resolves the tag correctly.
    TestScaleDownNoRecreate
    # Podman 4.3 reports a stopped container as "stopped" before its cleanup
    # settles on "exited"; the scenario asserts the Docker state once.
    TestUpProfile
    TestUpExitCodeFromContainerKilled
  )
fi

# Both modes use the resulting denylist: the remaining commit, bridge and
# interactive-hook cases run in rootless and rootful alike.
skip=$(IFS='|'; printf '^(%s)$' "${skip_tests[*]}")
cd pkg/e2e
# Serial: several tests share the alpine tags and one removes an image while
# another is resolving it; parallel runs also make the short-lived-service
# --wait cases miss their window. The rootless baseline was recorded serially.
run_e2e ../../bin/e2e.test -test.v -test.count=1 -test.parallel 1 -test.timeout 90m \
  -test.skip "$skip"

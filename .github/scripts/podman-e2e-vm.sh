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
# Runs the Podman e2e suite inside a throwaway Incus VM. The guest is a real
# kernel and systemd host, so the suite no longer needs the privileged
# systemd-in-container harness (and its cgroup/AppArmor/SELinux workarounds).
# Invoked by the e2e workflows; IMAGE names an Incus image and E2E_BIN points
# at the built compose binary.
set -Eeuo pipefail

: "${IMAGE:?IMAGE must name an Incus image, for example images:debian/12}"
: "${E2E_BIN:?E2E_BIN must point at the built compose binary}"

e2e_rootless="${E2E_ROOTLESS:-0}"
vm="podman-e2e-$$"
# Keep the runner's absolute workspace path in the guest: the test binary
# embeds it at compile time and gotest.tools reads the source file from that
# path when an assertion fails.
workdir="${GITHUB_WORKSPACE:-$PWD}"

cleanup() {
  sudo incus delete -f "$vm" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# The runner image ships Docker, whose FORWARD rules drop Incus bridge
# traffic. The workflow flushes the ruleset; keep the documented exception as
# a fallback when that step is skipped.
sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT 2>/dev/null || true
sudo iptables -I DOCKER-USER -o incusbr0 -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true

sudo incus launch "$IMAGE" "$vm" --vm \
  -c limits.cpu=2 -c limits.memory=6GiB -d root,size=16GiB

# incus launch returns when the VM is running, not when the agent answers.
for _ in $(seq 1 120); do
  sudo incus exec "$vm" -- true >/dev/null 2>&1 && break
  sleep 2
done
sudo incus exec "$vm" -- true

# Run from a copy inside the guest; .git is not used by the suite.
sudo incus exec "$vm" -- mkdir -p "$workdir"
tar -C "$workdir" --exclude=.git -cf - . | sudo incus exec "$vm" -- tar -C "$workdir" -xf -
sudo incus exec "$vm" -- chown -R 1000:1000 "$workdir"

rc=0
sudo incus exec "$vm" -- env \
  E2E_BIN="$workdir/bin/build/docker-compose" \
  E2E_ROOTLESS="$e2e_rootless" \
  STORAGE_DRIVER="${STORAGE_DRIVER:-overlay}" \
  DOCKER_CLI_VERSION="${DOCKER_CLI_VERSION:-27.5.1}" \
  DOCKERHUB_USERNAME="${DOCKERHUB_USERNAME:-}" \
  DOCKERHUB_TOKEN="${DOCKERHUB_TOKEN:-}" \
  WORKDIR="$workdir" \
  bash -c 'cd "$WORKDIR" && exec bash .github/scripts/podman-e2e.sh' || rc=$?

# The framework writes failure material to the guest's /tmp; pull it before
# deleting the VM so the workflow can upload it.
rm -rf "$PWD/artifacts"
if sudo incus file pull -r "$vm/tmp/compose-e2e-artifacts" "$PWD/artifacts" 2>/dev/null; then
  sudo chown -R "$(id -u):$(id -g)" "$PWD/artifacts"
fi

exit "$rc"

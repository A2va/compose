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
#
# Rebase the Podman overlay onto an upstream docker/compose release tag.
#
# This is the script behind both the local recipe in PODMAN.md and
# sync-upstream.yml, so the two cannot drift. It:
#   - fetches the tag into refs/upstream-tags/ (never touching the fork tags),
#   - enables rerere and loads the shared resolution cache,
#   - checks the PODMAN.md overlay index against the new upstream tree,
#   - rebases with fixup folding,
#   - verifies the three-layer overlay commit layout,
#   - on a conflict, writes a report (failing commit, conflicted paths and a
#     patch) and leaves the rebase in progress locally; in CI it aborts the
#     rebase so the branch stays clean.
#
# Usage:
#   podman-rebase.sh <upstream-tag>
#   podman-rebase.sh --save-rerere

set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "${root}"

UPSTREAM_REPO=${UPSTREAM_REPO:-docker/compose}
rerere_dir="$(git rev-parse --absolute-git-dir)/rr-cache"
rerere_cache=".github/podman/rerere"

save_rerere() {
	mkdir -p "${rerere_cache}"
	find "${rerere_dir}" -mindepth 1 -maxdepth 1 -type d 2>/dev/null |
		while IFS= read -r entry; do
			# Only resolved conflicts are useful, and whole-file conflicts (the
			# docs tables) are too context-dependent to replay: a stale cache
			# entry would silently replace a newer section.
			if [ ! -f "${entry}/postimage" ]; then
				continue
			fi
			if [ "$(wc -c <"${entry}/preimage")" -gt 16384 ]; then
				echo "skipping $(basename "${entry}"): whole-file preimage"
				continue
			fi
			cp -a "${entry}" "${rerere_cache}/"
		done
	# Drop cache entries that are no longer recorded.
	find "${rerere_cache}" -mindepth 1 -maxdepth 1 -type d 2>/dev/null |
		while IFS= read -r entry; do
			[ -d "${rerere_dir}/$(basename "${entry}")" ] || rm -rf "${entry}"
		done
	git add "${rerere_cache}"
	echo "Saved the rerere cache in ${rerere_cache}; commit it into the CI layer:"
	echo "  git commit --fixup='HEAD^{/^ci\\(podman\\):}' ${rerere_cache}"
}

load_rerere() {
	git config rerere.enabled true
	git config rerere.autoUpdate true
	if [ -d "${rerere_cache}" ]; then
		mkdir -p "${rerere_dir}"
		cp -a "${rerere_cache}/." "${rerere_dir}/"
		echo "Loaded the shared rerere cache from ${rerere_cache}."
	fi
}

verify_layout() {
	local upstream_ref=$1
	local subjects tip ci_count baseline baseline_count second
	subjects=$(git log --format=%s "${upstream_ref}..HEAD")
	tip=$(printf '%s\n' "${subjects}" | head -n1)
	ci_count=$(printf '%s\n' "${subjects}" | grep -c '^ci(podman):' || true)
	baseline='docs(podman): refresh the full-suite baseline'
	baseline_count=$(printf '%s\n' "${subjects}" | grep -Fxc "${baseline}" || true)
	if [ "${ci_count}" -ne 1 ] || ! printf '%s\n' "${tip}" | grep -q '^ci(podman):'; then
		echo "error: the overlay tip must be its single ci(podman): commit; got '${tip}'." >&2
		return 1
	fi
	if [ "${baseline_count}" -ne 1 ]; then
		echo "error: the overlay must contain exactly one '${baseline}' commit; found ${baseline_count}." >&2
		return 1
	fi
	second=$(printf '%s\n' "${subjects}" | sed -n '2p')
	if [ "${second}" != "${baseline}" ]; then
		echo "error: the baseline commit must sit directly below the ci(podman): commit; got '${second}'." >&2
		return 1
	fi
	echo "Overlay layout OK: ${ci_count} CI commit, baseline second."
}

if [ "${1:-}" = "--save-rerere" ]; then
	save_rerere
	exit 0
fi

tag=${1:?usage: podman-rebase.sh <upstream-tag> | --save-rerere}
report_dir=${PODMAN_REBASE_REPORT_DIR:-"$(git rev-parse --absolute-git-dir)/podman-rebase"}
mkdir -p "${report_dir}"

upstream_ref="refs/upstream-tags/${tag}"
if ! git rev-parse --verify -q "${upstream_ref}^{commit}" >/dev/null; then
	git fetch --force "https://github.com/${UPSTREAM_REPO}.git" \
		"refs/tags/${tag}:${upstream_ref}"
fi

load_rerere

if [ "${PODMAN_REBASE_SKIP_CHECK:-0}" != "1" ]; then
	check_report="${report_dir}/overlay-check.txt"
	if bash .github/scripts/podman-overlay-check.sh "${tag}" 2>"${check_report}"; then
		cat "${check_report}" >&2
	else
		cat "${check_report}" >&2
		{
			echo "Overlay index mismatch for ${tag}; the rebase will most likely"
			echo "conflict in the moved paths. Update the overlay files and the"
			echo "index in PODMAN.md:"
			cat "${check_report}"
		} >>"${report_dir}/summary.txt"
		if [ "${PODMAN_REBASE_STRICT_CHECK:-0}" = "1" ]; then
			exit 3
		fi
	fi
fi

# Fold fixup! commits into the ci(podman) and baseline layers.
if ! GIT_SEQUENCE_EDITOR=: GIT_EDITOR=: \
	git rebase -i --autosquash --no-keep-empty "${upstream_ref}"; then
	failing=$(git log -1 --format='%h %s' REBASE_HEAD 2>/dev/null || git rev-parse --short HEAD)
	{
		echo "Rebase onto ${tag} stopped on a conflict."
		echo "Failing commit: ${failing}"
		echo "Conflicted paths:"
		git diff --name-only --diff-filter=U | sed 's/^/  /'
		echo "Local resolution:"
		echo "  bash .github/scripts/podman-rebase.sh ${tag}"
		echo "  # resolve the conflicts, then:"
		echo "  git add -A && GIT_EDITOR=: git rebase --continue"
	} | tee "${report_dir}/summary.txt" >&2
	git diff --binary >"${report_dir}/conflict.patch" || true
	if [ "${CI:-}" = "true" ]; then
		git rebase --abort || true
	fi
	exit 2
fi

verify_layout "${upstream_ref}"

# Record the resolutions from this run for the next rebase. The cache belongs
# in the CI layer, so do not commit it automatically; tell the caller when the
# run added entries.
if [ -d "${rerere_dir}" ]; then
	echo "Rerere cache is in ${rerere_dir}; after resolving conflicts save it with"
	echo "  bash .github/scripts/podman-rebase.sh --save-rerere"
fi
echo "Rebase onto ${tag} complete. Unit tests and lint still need to run."

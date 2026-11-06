#!/usr/bin/env bash
#
# Check that the overlay index in PODMAN.md still maps onto an upstream tag.
#
# Every upstream file the Podman overlay patches is listed in the "Overlay
# index" table of PODMAN.md. Upstream package moves are the main cause of a
# rebase that conflicts in every commit that touched a path, so this script
# reads that table and, for each path missing from the target tree, follows the
# deletion/rename commit and prints the new location.
#
# Usage: podman-overlay-check.sh <upstream-tag>

set -euo pipefail

root=$(git rev-parse --show-toplevel)
cd "${root}"

tag=${1:?usage: podman-overlay-check.sh <upstream-tag>}
ref="refs/upstream-tags/${tag}"
if ! git rev-parse --verify -q "${ref}^{commit}" >/dev/null; then
	echo "error: ${ref} is not fetched; run the rebase script first" >&2
	exit 2
fi

# The first table under "Overlay index" is the rebase checklist. Stop at the
# blank line that closes it.
table=$(awk '
	/^\| File \| Change \|/ { in_table = 1 }
	in_table && /^$/ { exit }
	in_table { print }
' PODMAN.md)
if [ -z "${table}" ]; then
	echo "error: could not find the overlay index table in PODMAN.md" >&2
	exit 2
fi

# Follow a path deleted between the previous release and the target tag to its
# rename target, if upstream moved it.
rename_target() {
	local path=$1 commit
	commit=$(git log -1 --format=%H --diff-filter=DR "${ref}" -- "${path}" || true)
	if [ -n "${commit}" ]; then
		git show --name-status --format= --diff-filter=R "${commit}" |
			awk -v old="${path}" '$2 == old { print $3; exit }'
	fi
}

missing=0
checked=0
paths=$(
	printf '%s\n' "${table}" |
		awk -F'|' '{ print $2 }' |
		grep -o '`[^`]*`' |
		tr -d '`' |
		grep -E '(^|/)Makefile$|\.(go|md|ya?ml|sh)$' || true
)
if [ -z "${paths}" ]; then
	echo "error: the overlay index table has no file paths" >&2
	exit 2
fi

while IFS= read -r path; do
	[ -n "${path}" ] || continue
	checked=$((checked + 1))
	if git cat-file -e "${ref}:${path}" 2>/dev/null; then
		continue
	fi
	missing=$((missing + 1))
	target=$(rename_target "${path}")
	if [ -n "${target}" ]; then
		printf 'MOVED  %s -> %s\n' "${path}" "${target}" >&2
	else
		printf 'GONE   %s (no rename found in %s)\n' "${path}" "${tag}" >&2
	fi
done <<<"${paths}"

if [ "${missing}" -ne 0 ]; then
	cat >&2 <<EOF

${missing} of ${checked} overlay paths no longer exist in ${tag}.
Update the affected overlay files and the overlay index in PODMAN.md before
rebasing, so the replayed commits target the new paths.
EOF
	exit 1
fi

echo "Overlay index OK: ${checked} paths exist in ${tag}."

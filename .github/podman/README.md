# Podman rebase helpers

`rerere/` is a shared `git rerere` cache for overlay conflict resolutions. It is
empty until a maintainer resolves a conflict and records it:

```sh
# after finishing a rebase locally
bash .github/scripts/podman-rebase.sh --save-rerere
git commit --fixup='HEAD^{/^ci\(podman\):}' .github/podman/rerere
```

`podman-rebase.sh` loads the cache before rebasing, so a resolution recorded
once is replayed when the same conflict hunk comes back (for example on a patch
release after a failed sync). The cache is keyed by the conflict preimage, so
entries that no longer apply are harmless. Whole-file preimages (over 16 KiB,
typically the PODMAN.md baseline tables) are never saved: replaying those could
silently replace a newer section.

`podman-overlay-check.sh` verifies that every upstream path in the PODMAN.md
overlay index still exists in the target release and names rename targets. The
daily sync runs it before rebasing and includes its report in the conflict
issue, so an upstream package move is visible without re-reading 20 commits.

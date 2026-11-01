# Podman support — implementation plan

This fork makes `docker/compose` (the reference Compose implementation) run natively
against Podman v4+, with a minimal, rebase-friendly overlay. This document is the
agreed plan of record; the "Overlay index" section doubles as the rebase checklist.

Status (2026-11-01): P0 and P1 implemented — engine detection, socket
defaulting, classic-build forcing, registry digest fallback, link emulation,
secret ownership and the P1 compat shims (port ranges, `json-file`,
`dockerfile_inline`, secrets/configs). The full-suite baseline and remaining
engine gaps are documented below.

## Goal

- Full Compose-spec behavior on Podman v4 (incl. Debian 12's 4.3), v5, and v6.
- Smallest possible diff vs upstream; long-term maintainability wins over minimal
  changes when they conflict.
- Docker support is *not required* to be preserved, but it is preserved for free
  (all changes are behind Podman detection) — this is what keeps rebases trivial.

## Recommendation

**Do not write a Podman-native backend. Run compose against Podman's
Docker-compatible API (`podman system service`) and add a thin "Podman engine
profile": socket auto-selection, build strategy, and a small set of compat
shims.**

Rationale:

1. Compose v5 talks to the engine exclusively through docker/cli's
   `command.Cli` + the moby `client.APIClient` (`pkg/compose/compose.go:280`)
   and negotiates the API version at ping. The pinned client
   (`moby/moby/client v0.6.2`) speaks **1.40–1.56** — exactly covering what
   Podman reports on every target version (see matrix below).
2. Podman's compat service is a first-class, documented way to run compose
   (`podman-system-service(1)` literally shows `DOCKER_HOST=...podman.sock` +
   `docker-compose up`). It has worked in the field since Podman 4.x.
3. Compose is already defensive: every >1.44 engine feature is gated on the
   *negotiated* version with working fallbacks (multi-endpoint create →
   post-create `NetworkConnect`, manifests query params, …). On Podman the
   negotiation lands at 1.40/1.41/1.44 and the fallbacks engage automatically.
4. `podman-compose` (Python) re-implements the spec and lags by design;
   even the Podman community's de-facto answer is "use docker-compose against
   the podman socket". A fork of compose gives Podman users the *reference*
   implementation — the full spec, at upstream speed.

## Compat matrix (verified)

| Podman | Reports Docker API | Status in this fork |
|---|---|---|
| 4.3.x (Debian 12) | 1.40 | supported |
| 4.4–4.9 | 1.40/1.41 | supported |
| 5.0–5.7 | 1.41 | supported |
| 5.8+ / 6.x | 1.44 | supported |

## Alternatives considered (rejected)

1. **Native libpod REST backend implementing `client.APIClient`** — ~100+
   endpoints to reimplement/track; every upstream feature lands as a conflict.
   Rebase death. (The `DryRunClient` decorator in `pkg/dryrun/` proves the seam
   is swappable — we just don't need to pay for it.)
2. **Contribute Podman support upstream** — docker/cli and moby client are
   Docker-internal; upstream won't carry Podman-specific shims.
3. **Keep using `podman-compose`** — separate codebase, spec lag (e.g. no
   `cp`), maintenance is intermittent; doesn't meet "full spec" requirement.
4. **Compat socket + thin profile (chosen)** — engine does the compat work;
   the fork only smooths the deltas listed below.

## Architecture: where the overlay hooks in

The engine seam is `command.Cli` → `client.APIClient`; compose never talks to
the engine any other way (bake is the one exception: it `exec`s the buildx
binary, `pkg/compose/build_bake.go:182`).

**Detection** — `pkg/compose/podman.go`:
- `COMPOSE_CONTAINER_ENGINE=podman|docker` env var (explicit override), else
- a binary named `podman-compose` (this fork's packaging) defaults to the
  Podman socket, while `docker-compose`/plugin invocations keep Docker's
  default and never get hijacked;
- `isPodman()`, used by the shims, additionally treats any `DOCKER_HOST`
  containing `podman` as Podman. `podman compose` exports exactly that, so
  wrapper-driven runs are detected too. Detection stays API-free on purpose:
  an `/info` probe would force every build-path unit test to stub one more
  call.

**Overlay index — upstream files touched (the rebase checklist):**

| File | Change |
|---|---|
| `cmd/main.go` | Call `compose.ConfigurePodmanHost()` before `plugin.Run`. That helper (`pkg/compose/podman.go`) defaults `DOCKER_HOST` to the rootless/rootful Podman socket when the profile is on and no `DOCKER_HOST`/`DOCKER_CONTEXT`/`-H`/`--host`/`--context` is set; explicit `podman` with no socket exits with an actionable message. |
| `pkg/compose/build_bake.go` | If Podman, return classic (`POST /build?version=1`, served by buildah). Podman has no `/grpc` or `/session`, so bake cannot work; without buildx upstream already falls back, this makes it deterministic. |
| `pkg/compose/build_classic.go` | Materialize `build.dockerfile_inline` to a temp file for the classic builder (it otherwise requires a Dockerfile on disk). |
| `pkg/compose/create.go` | Podman-only: expand published port ranges by replica index (`podmanHostPort`), map `json-file` to `k8s-file`, mount an anonymous volume at `/run/secrets` for secrets (`podmanSecretVolume`), skip structured secret/config mounts, skip legacy `links` and publish link aliases on the target's endpoints (`podmanLinkAliases`). |
| `pkg/compose/pull.go` | If Podman, resolve image digests via `registry.RemoteDigest` (`internal/registry/remote.go`) because Podman has no `GET /distribution/{name}/json`. |
| `pkg/compose/secrets.go` | Tar entries use relative names and include parent directories, and Podman skips `copyUIDGID` so the declared uid/gid/mode survive extraction. |
| `pkg/e2e/framework.go` | Pass `DOCKER_HOST` through to e2e commands, like `DOCKER_CONTEXT`. |

All other logic lives in new files (`pkg/compose/podman*.go`, `PODMAN.md`), which never conflict on rebase.

**Podman-only helpers (new files):**
- `podman.go` — profile detection, socket selection, `isPodman()`.
- `internal/registry/remote.go` — minimal registry client (Bearer challenge, OCI/Docker
  manifest `Accept` headers, body hashing) used by digest resolution. No new
  dependencies.
- `podman_secrets.go` — anonymous volume at `/run/secrets` so archive injection
  reaches Podman's tmpfs-backed secrets directory.
- `podman_links.go` — emulates legacy `links` as network aliases on the link
  target (Podman rejects `HostConfig.Links`). The alias is resolvable by every
  container on the shared network, a superset of Docker's per-source entry.

## Known Podman gaps (documented, not worked around)

These are engine limitations, not missing compose features:

- **Docker-only log drivers** (e.g. `fluentd`) — Podman supports
  `k8s-file`/`journald`/`none`/`passthrough`; `json-file` is mapped to
  `k8s-file`.
- **BuildKit-only build features** (multi-arch, build secrets, ssh,
  additional contexts, builder selection) — classic builder limitation;
  upstream already prints a clear error. Podman builds with buildah.
- **Build log format** — tests asserting Docker's classic "naming to … done"
  output do not match buildah's output, although the build succeeds.
- **Buildah cache and build args** — buildah ignores a changed build-arg when
  the affected step is cacheable (verified: same image id until `--no-cache`),
  so compose cannot tell a rebuilt image from the previous one.
- **Legacy `links` on the default bridge** — Podman's default bridge has no
  DNS or alias support, so link aliases only work on user-defined networks.
- **Static network addresses** (`ipv4_address`) — Podman's compat create does
  not apply them; containers need a post-create connect.
- **Dependent-container removal** — Podman refuses to remove a container that
  other containers share a netns with, where Docker force-removes.
- **Restart backoff state** — Podman does not expose Docker's
  `State.Restarting` backoff to clients.
- **Watch sync onto existing paths** — Podman's copier errors when a tar
  directory entry already exists instead of merging.
- **`logs --follow` for containers created later** — the follow stream does
  not pick up replicas created after it started.
- **One-off removal race** — Podman reaps `run --rm` containers fast enough
  that a concurrent monitor can observe a missing container.
- **Inspect network mode** — Podman reports `HostConfig.NetworkMode` as
  `bridge` for containers attached to an external network.
- **Swarm features, `compose alpha` provider/relay/bridge, Docker Desktop** —
  not applicable to Podman; `TestProvider*`, `TestDependsOnMultipleProviders`
  and the bridge convert tests are excluded.

Environment-dependent failures (not Podman bugs, but the CI container has no
systemd and local runs can be rootless):

- **Healthchecks** — Podman 4.3 schedules them with systemd timers; without
  systemd they never leave `starting` (`TestLocalComposeUp`).
- **Ping-based tests in rootless mode** — rootless containers lack
  `CAP_NET_RAW` here (`TestWatchExec`, `TestUpStopWithLogsMixed`); they pass
  with rootful Podman in CI.
- **Fixed subnets** — tests hard-coding `192.168.0.0/16` collide with the host
  network of the test machine (`TestNetworkConfigChanged`).
- **SELinux relabel mode** — `TestLocalComposeVolume` asserts the inspect
  `Mode:"z"` field, which only exists with SELinux labelling enabled.

### Full-suite baseline (Podman 5.8.9 rootless, 2026-11-01)

`go test -tags e2e ./pkg/e2e` → **183 pass / 47 fail / 9 skip**. Every failure
falls into the categories above:

- BuildKit/buildx/build-log: `TestBuild*`, `TestComposeRunBuild`,
  `TestRunBuildOnce{Dependency,NestedDependencies,NoDeps}`.
- Alpha provider/relay/bridge: `TestProvider*`,
  `TestDependsOnMultipleProviders`, `TestBridgeConvertOutputNotEmptyConfirmed`,
  `TestConvertAndTransformList`, `TestConvertBuildOnlyService`.
- Engine behavior: `TestIPAMConfig`, `TestLocalComposeLogsFollow`,
  `TestNetworkLinks`, `TestNoDepsNetworkMode`, `TestScaleDownNoRecreate`,
  `TestUpAttachedTerminatesOnExternalStop`, `TestWatchSyncIntoSymlinkedDirectory`,
  `TestCascadeIgnoresOneOffContainer`, `TestRebuildOnDotEnvWithExternalNetwork`,
  `TestAttachRestart`, `TestLoggingDriver`.
- Environment/flaky on this host: `TestLocalComposeUp` (no systemd),
  `TestUpStopWithLogsMixed`/`TestWatchExec` (rootless ping),
  `TestWatchMultiServices` (watch timing), `TestNetworkConfigChanged`
  (host subnet), `TestLocalComposeVolume` (SELinux mode field),
  `TestExposeRange` (passes when rerun).

The CI script runs the passing Podman-relevant subset; expanding it to the
whole suite requires either fixing the engine gaps above or marking these as
skips with the reasons recorded here.

## Known hazards (tracked, with mitigations)

- **H1 — upstream bumps the moby client floor.** Docker CLI 29-era clients
  already reject servers < 1.44 (containers/podman#27890); our pinned
  `moby/moby/client v0.6.2` floor is verified at 1.40. On rebase, if
  `MinAPIVersion` rises above Podman's reported version, pin the previous
  client module in `go.mod` (or carry a one-line patch) and note it here.
- **H2 — Podman 4.3 build endpoint rejects some classic-builder query params**
  (containers/podman#15928, fixed in 4.4). The CI build smoke runs on the
  Debian 12 leg and passes, so the parameter set sent by the pinned client is
  accepted. Keep an eye on it when bumping the moby client.
- **H3 — resolved:** Podman mounts a tmpfs at `/run/secrets` (so plain writes
  to a stopped container are shadowed) and treats `copyUIDGID=true` as "chown
  to the daemon user". Secrets now go through an anonymous volume and the
  tar-declared ownership, so uid/gid/mode match Docker.
- **H4 — resolved:** events/log filters behave; validated with the e2e suite.

## Fork & rebase strategy

- Branch model: `main` tracks upstream `docker/compose` tags; `podman` (or
  `podman/main`) is our release branch, rebased onto each upstream tag.
  `upstream` remote, never merge — rebase keeps our hunks visible.
- **All Podman logic lives in new `podman*.go` files**; upstream files get
  only the small guarded hunks in the overlay index above.
- Rebase recipe: `git fetch upstream && git rebase upstream/vX.Y` → expect
  conflicts only in the files listed above → `make build test` + e2e vs Podman
  → update this doc → done.
- No renames, no reformats, no drive-by fixes in upstream files (per
  CONTRIBUTING.md) — every extra touched line is a future conflict.
- Binary/deployment: the standalone binary (`plugin.RunningStandalone()` in
  `cmd/main.go`) already accepts compose-style args, so
  `containers.conf(5)` `compose_providers = ["/usr/local/bin/podman-compose"]`
  wires `podman compose ...` straight to it. No wrapper code needed.

## Testing

- **Unit**: existing gomock pattern (`pkg/mocks/`, `make mocks`); detection
  is env/host-based so `pkg/compose/podman_test.go` needs no API mocks, and
  the build path keeps its existing stubs untouched.
- **E2E against Podman**:
  `HOME=<dir-with-.docker/config.json> DOCKER_HOST=unix:///run/podman/podman.sock
  go test -tags e2e ./pkg/e2e -run '<tests>'`. Notes:
  - the harness copies `$HOME/.docker/config.json` only when it is missing
    (upstream quirk) — provide a minimal `{}` file;
  - if `podman system service` cannot reach journald (e.g. CI sandbox), start
    it with a `containers.conf` setting `log_driver = "k8s-file"`.
- Validated against Podman 5.8.9: up/ps/copy/exec/logs/start/stop/events/
  networks/wait/healthcheck/pull/port-range/build/secrets-from-include pass.
  Expected failures are the documented gaps above.
- `golangci-lint run --build-tags "e2e" ./...` after every change (AGENTS.md).

## Work plan

- **P0 (implemented)** — detection + socket defaulting + classic-build forcing.
- **P1 (implemented)** — registry digest fallback, port ranges, `json-file`,
  `dockerfile_inline`, secret ownership (uid/gid/mode), `links` via network
  aliases; full-suite baseline documented above.
- **P2** — Debian 12 / Podman 4.3 validation job; CI for the matrix.
- **P3** (optional) — Podman niceties: rootless hints in errors, socket
  auto-activation check, `podman build` shell-out for multi-arch if ever
  needed.

## Overlay commit layout

`podman` is a patch stack on top of the upstream release, kept in three
layers from bottom to top:

1. **Overlay commits** — every Podman-specific feature, compat shim, fix and
   docs change, including the unit tests and the `pkg/e2e/` cases that cover
   them. One logical change per commit.
2. **`docs(podman): refresh the full-suite baseline`** — **only `PODMAN.md`**:
   the recorded baseline and its gap/environment notes. It changes no code,
   harness or script. Exactly one commit, directly below the CI commit.
3. **`ci(podman): …`** — workflows, CI and the harness/scripts they invoke
   (`.github/workflows/*`, `.github/scripts/*`, CI build config), including the
   e2e denylist. Exactly one commit, always the tip of the branch.

A rerun's recorded results go in the baseline commit; changes to the harness or
scripts that produced them go in the CI commit, and e2e test changes go in the
overlay commit whose behavior they cover.

Amend the layer a change belongs to; never add a commit above the CI one.
Instead of an interactive rebase, commit a fixup and let the next rebase fold
it in:

```sh
# Workflows, CI and harness/scripts:
git commit --fixup='HEAD^{/^ci\(podman\):}'
# PODMAN.md baseline only:
git commit --fixup='HEAD^{/^docs\(podman\): refresh the full-suite baseline}'
GIT_SEQUENCE_EDITOR=: git rebase -i --autosquash upstream/vX.Y.Z
```

`sync-upstream.yml` performs the same autosquash, then refuses to test or
release unless the tip is the `ci(podman):` commit and the baseline commit is
directly below it. Releases are annotated tags on that CI tip, so the branch
has no release-marker commit above the stack.

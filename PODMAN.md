# Podman support — implementation plan

This fork makes `docker/compose` (the reference Compose implementation) run natively
against Podman v4+, with a minimal, rebase-friendly overlay. This document is the
agreed plan of record; the "Overlay index" section doubles as the rebase checklist.

Status (2026-11-01): P0–P4 implemented — engine detection, socket defaulting
and probing with rootless hints, classic-build forcing, registry digest
fallback, e2e `DOCKER_HOST` support, link emulation, secret ownership, the P1
compat shims (port ranges, `json-file`, `dockerfile_inline`, secrets/configs),
build-arg image identity, watch sync into existing paths, one-off/log-attach
races, default-bridge link injection, log-driver fallback and a CI matrix that
runs the e2e suite against Podman 4.3 (Debian 12), 4.9 (Ubuntu 24.04) and
current Fedora. Known Podman gaps are listed below.

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
| 4.3.x (Debian 12) | 1.40 | supported; CI leg `podman-4.3-debian-12` |
| 4.4–4.9 | 1.40/1.41 | supported; CI leg `podman-4.9-ubuntu-24.04` |
| 5.0–5.7 | 1.41 | supported |
| 5.8+ / 6.x | 1.44 | supported; CI leg `podman-current-fedora` |

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
- socket selection dials each candidate with a short timeout (this also
  triggers systemd socket activation). A missing, stale or inaccessible socket
  fails with rootless/rootful enable hints instead of a bare engine error.

**Overlay index — upstream files touched (the rebase checklist):**

| File | Change |
|---|---|
| `cmd/main.go` | Call `compose.ConfigurePodmanHost()` before `plugin.Run`. That helper (`pkg/compose/podman.go`) defaults `DOCKER_HOST` to the rootless/rootful Podman socket when the profile is on and no `DOCKER_HOST`/`DOCKER_CONTEXT`/`-H`/`--host`/`--context` is set; explicit `podman` with no responding socket exits with rootless/rootful enable hints. |
| `pkg/compose/build_bake.go` | If Podman, return classic (`POST /build?version=1`, served by buildah). Podman has no `/grpc` or `/session`, so bake cannot work; without buildx upstream already falls back, this makes it deterministic. |
| `pkg/compose/build_classic.go` | Materialize `build.dockerfile_inline` to a temp file for the classic builder (it otherwise requires a Dockerfile on disk), and fingerprint build args into an image label so buildah's cache cannot hide a changed arg behind an unchanged image ID. |
| `pkg/compose/create.go` | Podman-only: expand published port ranges by replica index (`podmanHostPort`), map `json-file` to `k8s-file`, mount an anonymous volume at `/run/secrets` for secrets (`podmanSecretVolume`), skip structured secret/config mounts, skip legacy `links` and publish link aliases on the target's endpoints (`podmanLinkAliases`). |
| `pkg/compose/pull.go` | If Podman, resolve image digests via `registry.RemoteDigest` (`internal/registry/remote.go`) because Podman has no `GET /distribution/{name}/json`. |
| `pkg/compose/secrets.go` | On Podman, tar entries use relative names and include parent directories, and `copyUIDGID` is skipped so the declared uid/gid/mode survive extraction. |
| `internal/inspect/ps.go` | Podman <1.44 omits EXPOSE'd ports from `/containers/json` and the host IP from published ports; restore both for `compose ps`/`port`. |
| `pkg/compose/monitor.go` | Filter one-off containers from the initial set and the event loop: Podman does not always apply the event stream's one-off label filter, which made a `compose run` exit abort an attached `up`. |
| `pkg/compose/up.go`, `pkg/compose/logs.go` | Treat a container removed between its start event and log attach as already finished instead of failing `up`/`logs --follow`. |
| `internal/sync/tar.go` | Retry without implied directory headers when the engine reports `file exists` (Podman's copier), in addition to Docker's symlink error; when Podman 6 still reports `path escapes from parent`, resolve the target and copy there. |
| `pkg/compose/watch.go` | `ResolvePath` runs `readlink -f` in the container so sync can write through a symlink Podman 6 refuses to traverse. |
| `pkg/e2e/framework.go` | Pass `DOCKER_HOST` through to e2e commands, like `DOCKER_CONTEXT`, and copy the host `config.json` into the per-test config dir (the upstream check was inverted, which breaks podman-docker's CLI wrapper). |
| `.github/workflows/ci.yml` | Publish fork releases from `podman-v*` tags (non-draft) and skip upstream `v*` releases in the fork; its existing binary/SBOM/provenance/signing jobs build the assets. |

All other logic lives in new files (`pkg/compose/podman*.go`, `PODMAN.md`,
`.github/*`), which never conflict on rebase.

**Podman-only helpers (new files):**
- `podman.go` — profile detection, socket selection/probing with rootless
  hints, `isPodman()`.
- `internal/registry/remote.go` — minimal registry client (Bearer challenge, OCI/Docker
  manifest `Accept` headers, body hashing) used by digest resolution. No new
  dependencies.
- `podman_secrets.go` — anonymous volume at `/run/secrets` so archive injection
  reaches Podman's tmpfs-backed secrets directory.
- `podman_links.go` — emulates legacy `links` as network aliases on the link
  target (Podman rejects `HostConfig.Links`). The alias is resolvable by every
  container on the shared network, a superset of Docker's per-source entry.
  On the default bridge, which has no DNS, the same aliases are added to the
  source's `/etc/hosts` after start.

## Known Podman gaps (documented, not worked around)

These are engine limitations, not missing compose features:

- **Docker-only log drivers** (e.g. `fluentd`) — Podman supports
  `k8s-file`/`journald`/`none`/`passthrough`; `json-file` and any other
  unsupported driver fall back to `k8s-file` with a warning, so logs stay
  local instead of reaching the configured sink.
- **BuildKit-only build features** (multi-arch, build secrets, ssh,
  additional contexts, builder selection) — classic builder limitation;
  upstream already prints a clear error. Podman builds with buildah.
- **Build log and error text** — tests asserting Docker's classic
  "naming to … done" output or `No such image: …` wording do not match
  buildah's output, although the build succeeds.
- **Default-bridge links in shell-less images** — the aliases are appended to
  `/etc/hosts` with `sh` after start; scratch/distroless images keep the old
  no-DNS behavior. An app resolving names during its first instruction can
  still race the injection.
- **Static network address metadata** — the address is assigned, but Podman
  reports `IPAMConfig` as `null` in inspect; assert `IPAddress` instead.
  (`TestIPAMConfig` still reads the metadata field.)
- **Stale image identity on Podman 4.3** — after a rebuild, the tag can still
  resolve to the previous image ID, so compose keeps the old containers
  (`TestScaleDownNoRecreate`). Current Podman resolves it correctly, so the
  build-arg identity label is only exercised there.
- **Dependent-container removal** — Podman refuses to remove a container that
  other containers share a netns with, where Docker force-removes.
- **Restart backoff state** — Podman does not expose Docker's
  `State.Restarting` backoff to clients.
- **Inspect network mode** — Podman reports `HostConfig.NetworkMode` as
  `bridge` for containers attached to an external network.
- **Swarm features, `compose alpha` provider/relay/bridge, Docker Desktop** —
  not applicable to Podman; `TestProvider*`, `TestDependsOnMultipleProviders`
  and the bridge convert tests are excluded.

Environment-dependent failures (not Podman bugs, but the CI container has no
systemd and local runs can be rootless):

- **Healthchecks** — Podman 4.3 schedules them with systemd timers; without
  systemd they never leave `starting` (`TestLocalComposeUp`).
- **Fixed subnets** — tests hard-coding `192.168.0.0/16` collide with the host
  network of the test machine (`TestNetworkConfigChanged`).
- **SELinux relabel mode** — `TestLocalComposeVolume` asserts the inspect
  `Mode:"z"` field, which only exists with SELinux labelling enabled.
- **Logs-follow cold start** — `TestLocalComposeLogsFollow` gives the first
  stream 1s to produce a line; Podman can take longer on a loaded host. The
  follow itself, including replicas created later, works.
- **Rootless host file permissions** — tests bind-mount 0600 files under the
  test temp dir, which the rootless container user cannot read
  (`TestComposeRun`, `TestLocalComposeLargeLogs`, `TestStdoutStderr`).

### Full-suite baseline (Podman 5.8.9 rootless, 2026-11-01)

Whole suite, serial, with `TMPDIR` under `/tmp/opencode`:

`go test -tags e2e ./pkg/e2e -parallel 1` → **186 pass / 42 fail / 11 skip**
before the default-bridge link injection and log-driver fallback; those fixes
make `TestNetworkLinks` and `TestLoggingDriver` pass, so the expected baseline
is **188 pass / 40 fail / 11 skip**. The skips are optional Docker Desktop,
model/buildx and multi-platform cases. Every remaining failure falls into the
categories above:

- BuildKit/buildx/build-log: `TestBuild*`, `TestComposeRunBuild`,
  `TestRunBuildOnce{Dependency,NestedDependencies,NoDeps}`.
- Alpha provider/relay/bridge: `TestProvider*`,
  `TestDependsOnMultipleProviders`, `TestBridgeConvertOutputNotEmptyConfirmed`,
  `TestConvertAndTransformList`, `TestConvertBuildOnlyService`.
- Engine behavior: `TestIPAMConfig`, `TestLocalComposeLogsFollow`,
  `TestNoDepsNetworkMode`, `TestUpAttachedTerminatesOnExternalStop`,
  `TestRebuildOnDotEnvWithExternalNetwork`.
- Environment on this host: `TestLocalComposeUp` (no systemd),
  `TestNetworkConfigChanged` (host subnet), `TestLocalComposeVolume`
  (SELinux mode field), `TestComposeRun`/`TestLocalComposeLargeLogs`/
  `TestStdoutStderr` (rootless file permissions).

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
- CI:
  - `.github/workflows/sync-upstream.yml` automates the rebase recipe above.
    Daily it checks whether the latest upstream release tag is already in
    `podman`; on a new tag it rebases `podman` onto that tag, runs the unit
    suite and the e2e matrix below, then force-pushes `podman`, tags
    `podman-vX.Y.Z`, and dispatches `ci.yml` at that tag. `ci.yml` reuses its
    existing binary and finalize jobs to build the cross-platform binaries,
    named as on docker/compose (`docker-compose-linux-x86_64`, ...), with SBOM
    (`.sbom.json`), SLSA provenance (`.provenance.json`), signature
    (`.sigstore.json`), `.sha256` and a `checksums.txt`, then publishes the
    release. The release branch is exactly the upstream release with the
    overlay on top: the workflow adds no bookkeeping commits and fetches only
    the tag, never upstream's branches. A failed rebase or test regression
    leaves `podman` and the releases untouched and files an issue instead. The
    upstream `v*` tag release path in `ci.yml` stays a draft and is skipped in
    the fork. Set the repo to allow GitHub Actions to create and dispatch
    releases.
  - `.github/workflows/podman-e2e.yml` runs the e2e smoke suite against the
    Podman matrix. The static test binary executes inside a privileged
    container (`debian:12`, `ubuntu:24.04`, `fedora:latest`), so old Podman
    versions need no host compatibility. The Fedora leg uses vfs storage and
    a tmpfs for `/var/tmp`: Fedora 45's buildah rejects an overlay over a
    build context on the container's overlay root. Optional
    `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` secrets raise Docker Hub pull
    limits.

## Testing

- **Unit**: `go test ./pkg/...`; detection and socket probing are
  env/host-based, so `pkg/compose/podman_test.go` needs no API mocks (socket
  tests use a local unix listener), and the build path keeps its existing
  stubs untouched.
- **E2E against Podman**: the CI script
  `.github/scripts/podman-e2e.sh` builds a static test binary and runs the
  validated subset (up/ps/copy/exec/logs/start/stop/events/networks/wait/
  pull/port-range/secrets/links, plus watch sync, cascades (including a
  concurrent one-off), expose ranges, hooks, restart and network modes) and a
  classic-build smoke on every Podman version. Known gaps above are excluded
  by design. The log-driver fallback is exercised on a host with a real Docker
  CLI: `TestLoggingDriver` probes `docker info -f {{.OperatingSystem}}`, which
  podman-docker's wrapper does not implement.
- **Local runs** use the same script:
  `CGO_ENABLED=0 make build GO_BUILDTAGS="e2e standalone" && CGO_ENABLED=0 go
  test -c -tags "e2e standalone" -o bin/e2e.test ./pkg/e2e` then
  `docker run --rm --privileged -v "$PWD:$PWD" -w "$PWD"
  -e E2E_BIN="$PWD/bin/build/docker-compose" debian:12
  bash "$PWD/.github/scripts/podman-e2e.sh"`. Notes:
  - the e2e harness copies `$HOME/.docker/config.json` only when it is missing
    (upstream quirk) — the script provides a minimal `{}` file;
  - nested podman uses `vfs` storage in the script for reliability.
- `golangci-lint run --build-tags "e2e" ./...` after every change (AGENTS.md).

## Work plan

- **P0 (implemented)** — detection + socket defaulting + classic-build forcing.
- **P1 (implemented)** — registry digest fallback, e2e `DOCKER_HOST` support,
  port ranges, `json-file`, `dockerfile_inline`, secret ownership
  (uid/gid/mode), `ps` port display, `links` via network aliases; e2e smoke
  matrix green except the documented engine gaps.
- **P2 (implemented)** — CI matrix for Podman 4.3 (Debian 12), 4.9 (Ubuntu
  24.04) and current Fedora, with a classic-build smoke on each version.
- **P3 (implemented)** — close the compose-side gaps found by the suite:
  build-arg image identity despite buildah's cache, watch sync into existing
  paths, one-off event filtering, log attach to already-removed containers,
  default-bridge link injection and unsupported log-driver fallback; widen the
  CI subset with the tests this unlocked.
- **P4 (implemented)** — probe the resolved socket at startup (which also
  triggers systemd socket activation) and report rootless/rootful setup hints
  when it is missing, stale or inaccessible. No `podman build` shell-out:
  multi-platform builds stay a BuildKit-only feature with a clear
  classic-builder error, so the shell-out is deferred until it is needed.

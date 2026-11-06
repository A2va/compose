# Podman support — implementation plan

This fork makes `docker/compose` (the reference Compose implementation) run natively
against Podman v4+, with a minimal, rebase-friendly overlay. This document is the
agreed plan of record; the "Overlay index" section doubles as the rebase checklist.

Status (2026-11-05): P0–P6 implemented — engine detection, socket defaulting
and probing with rootless hints, classic-build forcing, registry digest
fallback, e2e `DOCKER_HOST` support, link emulation, secret ownership, the P1
compat shims (port ranges, `json-file`, `dockerfile_inline`, secrets/configs),
build-arg image identity, watch sync into existing paths, one-off/log-attach
races, default-bridge link injection, log-driver fallback, the `compose ps`
health suffix and a CI matrix that runs the e2e suite against Podman 4.3
(Debian 12), 4.9 (Ubuntu 24.04) and current Fedora, in both rootless and
rootful modes. Known Podman gaps are listed below.

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
| `cmd/compose/run.go` | Materialize the implicit `default` network at project level when a job is turned into a runnable service: compose-go leaves it unresolved for a job-only project, and Podman <5 rejects the empty network name the create path would otherwise send. |
| `pkg/compose/build_bake.go` | If Podman, return classic (`POST /build?version=1`, served by buildah). Podman has no `/grpc` or `/session`, so bake cannot work; without buildx upstream already falls back, this makes it deterministic. |
| `pkg/compose/build_classic.go` | Materialize `build.dockerfile_inline` to a temp file for the classic builder (it otherwise requires a Dockerfile on disk), fingerprint build args into an image label so buildah's cache cannot hide a changed arg behind an unchanged image ID, and tag built images with the standard project/service/version labels (lost upstream when the internal BuildKit builder was removed) so `down --rmi` can find them. On Podman with a local or cloned context the standard labels are injected as deterministic Dockerfile instructions, because buildah randomizes the order of labels passed in the build request and would change the image ID on every rebuild; remote contexts keep the build-request labels since they have no local Dockerfile to rewrite. |
| `pkg/compose/create.go` | Podman-only: expand published port ranges by replica index (`podmanHostPort`), map `json-file` to `k8s-file`, mount an anonymous volume at `/run/secrets` for secrets and configs targeting it (`podmanFileReferenceVolume`), skip structured secret/config mounts, skip legacy `links` and publish link aliases on the target's endpoints (`podmanLinkAliases`). |
| `pkg/compose/reconcile.go` | Accept the network name Podman <5 reports in `NetworkID` as equivalent to the engine-assigned network ID, so an unchanged running container is not recreated on every `up`/scale. |
| `pkg/compose/executor_ops.go` | Podman-only: for an attached (interactive) up, inject secrets/configs at container creation, because an attach stream opened before the start phase makes Podman extract the archive into the root filesystem instead of the `/run/secrets` volume. Detached up, create and start inject right before the start, as Docker does, so `create` does not resolve environment secrets early. |
| `pkg/compose/apiSocket.go` | On Podman, bind the engine's own API socket (from `DOCKER_HOST`) at `/var/run/docker.sock`, the path services expect. |
| `pkg/compose/pull.go` | If Podman, resolve image digests via `registry.RemoteDigest` (`internal/registry/remote.go`) because Podman has no `GET /distribution/{name}/json`. |
| `internal/relay/relay.go` | Probe whether the relay-link gateway is bindable by this host before announcing it: rootless engines (and rootful engines inside a container) may not own it, so the provider binds the wildcard instead. On Podman the relay dials `host.containers.internal`, which Podman provisions itself. |
| `pkg/compose/secrets.go` | On Podman, tar entries use relative names and include parent directories, and `copyUIDGID` is skipped so the declared uid/gid/mode survive extraction. |
| `internal/inspect/ps.go` | Podman <1.44 omits EXPOSE'd ports from `/containers/json` and the host IP from published ports, and Podman omits the health suffix Docker appends to the container status; restore all three for `compose ps`/`port`. |
| `pkg/compose/monitor.go` | Filter one-off and lifecycle-hook containers from the initial set and the event loop: Podman does not always apply the event stream's one-off label filter, and a hook runner's exit must not abort an attached `up`. |
| `pkg/compose/up.go`, `pkg/compose/logs.go` | Treat a container removed between its start event and log attach as already finished instead of failing `up`/`logs --follow`. |
| `internal/sync/tar.go` | Retry without implied directory headers when the engine reports `file exists` (Podman's copier), in addition to Docker's symlink error; when Podman 6 still reports `path escapes from parent`, resolve the target and copy there. |
| `pkg/compose/watch.go` | `ResolvePath` runs `readlink -f` in the container so sync can write through a symlink Podman 6 refuses to traverse. |
| `pkg/bridge/convert.go` | On a rootless engine (reported as `name=rootless` in `/info` SecurityOptions, or a per-user graph root on Podman 4.x), run the bridge transformer as container root: container root maps to the invoking user, so the transformer can read the 0600 input and write the output bind mounts. Rootful engines keep the historical host-uid setting. |
| `pkg/e2e/framework.go` | Pass `DOCKER_HOST` through to e2e commands, like `DOCKER_CONTEXT`, and copy the host `config.json` into the per-test config dir (the upstream check was inverted, which breaks podman-docker's CLI wrapper). |
| `pkg/e2e/compose_run_test.go` | Assert `run` port publication through `compose ps` rather than raw `docker ps`: Podman <1.44 omits the published port's host IP from the container summary, so the Docker CLI renders only the container port. |
| `.github/workflows/ci.yml` | Publish fork releases from `v*` tags when dispatched with `release=true` (non-draft) and skip upstream `v*` tag pushes in the fork; its existing binary/SBOM/provenance/signing jobs build the assets. |

All other logic lives in new files (`pkg/compose/podman*.go`, `PODMAN.md`,
`.github/*`), which never conflict on rebase.

**Podman-only helpers (new files):**
- `podman.go` — profile detection, socket selection/probing with rootless
  hints, `isPodman()`.
- `internal/inspect/podman_status.go` — restores the `(healthy)`,
  `(unhealthy)` or `(health: starting)` suffix Docker puts in the
  `/containers/json` status field, so the `compose ps` table matches Docker.
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

These are engine limitations, not missing compose features (compose-side
fallbacks are noted where they exist):

- **Docker-only log drivers** (e.g. `fluentd`) — Podman supports
  `k8s-file`/`journald`/`none`/`passthrough`; `json-file` and any other
  unsupported driver fall back to `k8s-file` with a warning, so logs stay
  local instead of reaching the configured sink.
- **BuildKit-only build features** (multi-arch, build secrets, ssh,
  additional contexts other than `service:` references in an inline
  Dockerfile, builder selection) — classic builder limitation; upstream
  already prints a clear error. Podman builds with buildah. Service references
  are rewritten to the image the classic builder produces for that dependency.
- **Build error text** — tests asserting Docker's classic `No such image: …`
  wording do not match buildah's output, although the build succeeds. Classic
  builds emit the `naming to … done` line Docker reports.
- **Default-bridge links in shell-less images** — the aliases are appended to
  `/etc/hosts` with `sh` after start; scratch/distroless images keep the old
  no-DNS behavior. An app resolving names during its first instruction can
  still race the injection.
- **Static network address metadata** — the address is assigned and Podman's
  network inspect reports it (`.Containers[*].IPv4Address`, via
  `convertLibpodNetworktoDockerNetwork`), but container inspect leaves the
  endpoint's `IPAMConfig` `null`: `GET /containers/{id}/json` uses
  `getContainerNetworkInfo` (`libpod/networking_common.go`), which never fills
  the `InspectAdditionalNetwork.IPAMConfig` field. Even a plain
  `docker run --ip` returns `null`, so this is not a compose-side gap.
  `TestIPAMConfig` reads the container field, so it cannot pass until either
  the test asserts `IPAddress` instead or Podman fills `IPAMConfig` from the
  stored static IPs.
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
- **Relay endpoint binding** — a rootless engine keeps its bridge networks in
  a user namespace, and a rootful engine running inside a container may not own
  the relay-link gateway either. Compose probes the gateway and announces none
  when this host cannot bind it; the provider falls back to the wildcard and
  the relay reaches it through `host.containers.internal` on Podman (Podman
  provisions that itself) or `host.docker.internal` on Docker. Rootful engines
  with a bindable gateway keep the gateway-bound path. Swarm, `compose alpha`
  features that are Docker-only, and Docker Desktop remain out of scope.

Environment-dependent failures (not Podman bugs, but a host without systemd
leaves healthchecks starting, and local runs can be rootless):

- **Healthchecks require systemd** — Podman schedules them with systemd
  transient timers, so on a host without systemd they never leave `starting`.
  The CI workflow boots systemd as PID 1 in the test container before running
  the suite, and the rootless mode enables lingering so the non-root user gets
  a systemd user manager for its transient timers. The local baseline below
  was recorded on a systemd host. The `compose ps` health suffix is restored
  for both 4.3 and 5.8.
- **Docker CLI compatibility probes** — CI installs the real Docker CLI
  (pinned to 27.5.1: Docker 29 raised the minimum API to 1.44, above Podman
  4.3's 1.40) instead of `podman-docker`, exactly as a local run against the
  Podman socket does. The wrapper's reduced commands and output types
  (`docker info` `DriverStatus`, `docker context create --docker` TLS options,
  the image-inspect `.ID` field, `docker top`) made the CLI-probe tests fail
  only in CI. With the real client the containerd-store and umask probes skip
  on Podman and `TestUpImageID` runs. `TestBuildTLS`, `TestLocalComposeUp` and
  `TestLoggingDriver` stay on the denylist for engine-side reasons: the first
  selects a Docker-in-Docker context while `DOCKER_HOST` still points at
  Podman (so the profile forces the classic builder), the second reads the
  compose `top` layout, and the third relies on the log-driver fallback.
- **Rootless mode (`E2E_ROOTLESS=1`)** — the CI container runs the suite as a
  non-root user whose uid matches the checkout, with subuid/subgid mappings
  and a lingering systemd user manager (`loginctl enable-linger`). That
  matches the rootless host baseline, so the commit/job-only/idempotent
  up/scale/interactive-hook cases run instead of being skipped. The bridge
  transformer also runs as container root in this mode (`pkg/bridge/convert.go`):
  container root maps to the invoking user, so it can read the 0600 input bind
  mount and write the output directory. Fedora 45's
  `shadow-utils-subid` package ships `newuidmap`/`newgidmap` without setuid
  or file capabilities, so the script grants them `cap_setuid,cap_setgid`
  with `setcap` before starting rootless Podman. The outer container also
  runs with AppArmor unconfined, but that only unconfines the container: on
  Ubuntu 26.04 the host's `pasta` profile still attaches when the nested
  engine execs `pasta` and rejects the SIGTERM that tears down the rootless
  network namespace, so every `down` fails with `rootless netns: kill network
  process: permission denied`. The harness adds the runner-image
  `signal (receive) set=(term) peer=podman,` rule to
  `/etc/apparmor.d/usr.bin.pasta` and reloads it when the profile is present
  and still lacks the rule (actions/runner-images#15184). Ubuntu 24.04 ships
  no pasta profile, and the rootful leg does not use the rootless netns.
- **Rootful mode (`E2E_ROOTLESS=0`)** — the matrix also runs every Podman
  version as root inside the test container, with the same denylist as
  rootless for that version. The cases that previously failed rootful (empty
  commit body, empty job network name, unnecessary recreate, timing and
  inspect differences) are run in both modes, so a failure is a real engine
  gap to fix rather than a reason to drop the rootful leg.
- **Version gaps (Podman 4.3/4.9)** — `TestBuildEscaped` fails in both modes
  on the older engines: their buildah predates Dockerfile heredocs, so the
  `RUN <<'EOF'` body is parsed as instructions. The other recreate, job-only
  network and port-display gaps are handled by the Podman profile:
  `pkg/compose/reconcile.go` accepts the network name Podman <5 reports in
  `NetworkID`, `cmd/compose/run.go` materializes the implicit default network
  for a job-only project, and `TestComposeRunPorts` asserts through `compose
  ps` instead of raw `docker ps` (Podman <1.44 omits the published port's host
  IP from the container summary). Podman 4.3 additionally skips `TestCommit`,
  `TestCommitWithReplicas`, `TestUpExitCodeFromContainerKilled` and
  `TestUpProfile`: its inspect state briefly reports `stopped` before cleanup
  settles on `exited`, and the profile scenario asserts the Docker state once.
  The three interactive-hook tests and `TestConvertAndTransformList` run on
  every version, as do the cases that previously failed only rootful.
- **Fixed subnets** — tests hard-coding `192.168.0.0/16` collide with the host
  network of the test machine (`TestNetworkConfigChanged`).
- **Anonymous-volume inspect mode** — `TestLocalComposeVolume` asserts the
  inspect `Mode:"z"` field Docker reports for an anonymous volume created
  through the mount API; Podman reports it empty. This is engine metadata, not
  a labelling requirement: Podman does report `z` when the mount asks for it.
- **Logs-follow cold start** — `TestLocalComposeLogsFollow` gives the first
  stream 1s to produce a line; Podman can take longer on a loaded host. The
  follow itself, including replicas created later, works.
- **Watch rebuild timing** — `TestWatchMultiServices` can time out with service
  `a` not running after a rebuild on a loaded host; the single-service watch
  sync tests pass.
- **SELinux-labelled bind mounts** — on an SELinux-enforcing host a direct run
  cannot read the 0600 files the tests bind-mount from their 0700 temp dirs
  (`TestComposeRun`, `TestLocalComposeLargeLogs`, `TestStdoutStderr`,
  `TestProjectVolumeBind`) unless the mount is relabelled. The CI harness runs
  with `--security-opt=label=disable`, so those tests run there. `bridge
  convert` used to hit the same gap, but its transformer now runs as container
  root in a rootless engine, which maps to the invoking user and can read the
  0600 `compose.yaml` in the 0700 temp dir.

### Full-suite baseline (Podman 5.8.9 rootless, 2026-11-05)

Whole suite, serial, with `TMPDIR` under `/tmp/opencode`, after `make build`
and `make example-provider`:

`go test -tags e2e ./pkg/e2e -parallel 1 -timeout 40m` → **221 pass / 30 fail /
10 skip** (261 top-level tests). The rootless relay fix landed after this run:
the two relay endpoint cases now pass too, so the current result is **223 pass /
28 fail / 10 skip**. With the provider fixture built, all ten provider cases
pass. The skips are optional Docker Desktop, model/buildx, containerd-store and
multi-platform cases. `TestLocalComposeUp` passes with the `compose ps` health suffix. The
`use_api_socket` and interactive hook cases pass after the fixes above.
`TestAttachRestart`, `TestNestedDotEnv`, `TestLocalComposeLogsFollow` and
`TestWatchMultiServices` passed in this run but remain timing-sensitive: the
restart/log-attach race, a Podman attach after the container exited, a loaded
host's cold start, and watch rebuild timing. Every failure in this run falls
into the categories above:

- BuildKit/buildx/build-log: `TestBuildAdditionalContextDisabledService`,
  `TestBuildBuilder`, `TestBuildDependentImage`,
  `TestBuildDependentImageWithProfile`, `TestBuildEntitlements`,
  `TestBuildImageDependencies`, `TestBuildPlatformsStandardErrors`,
  `TestBuildPlatformsWithCorrectBuildxConfig`, `TestBuildPrivileged`,
  `TestBuildSecrets`, `TestBuildSSH`, `TestBuildSubDependencies`,
  `TestComposeRunBuild` and `TestRunBuildOnce{Dependency,NestedDependencies,NoDeps}`.
- Bridge convert (rootless only; fixed after this baseline):
  `TestBridgeConvertOutputNotEmptyConfirmed`, `TestConvertAndTransformList`,
  `TestConvertBuildOnlyService`. The transformer now runs as container root in
  a rootless engine, so it can read the 0600 input bind mount; the CI container
  disables SELinux, so the relabel gap does not apply there. On a
  SELinux-enforcing host these still need a relabel or `label=disable`.
- Engine/watch behavior: `TestIPAMConfig`, `TestNoDepsNetworkMode`,
  `TestUpAttachedTerminatesOnExternalStop`,
  `TestRebuildOnDotEnvWithExternalNetwork`.
- SELinux on this host (direct run; these pass under the CI harness):
  `TestComposeRun`, `TestLocalComposeLargeLogs`, `TestStdoutStderr`,
  `TestProjectVolumeBind`.
- Anonymous-volume inspect mode: `TestLocalComposeVolume`.

The six build cases unskipped after this run pass on 5.8.9:
`TestBuildAdditionalContextDisabledService`, `TestBuildDependentImage`,
`TestBuildSubDependencies` and the three `TestRunBuildOnce*` cases.
`TestComposeRunBuild` builds, but its one-off attach still hangs intermittently,
so it stays on the denylist.

The two cases added by the v5.7 sync also run on Podman:
`TestFileReferenceOverridesWarning` passes because only an interactive up
injects file references at creation, so `create` leaves the missing
environment secret for the start; `TestDownImagesOfSelectedServices` passes
on 4.9/current because classic-built images carry the project/service labels,
so `down --rmi local` finds the selected service's image. On Podman the same
labels are written into the Dockerfile, so they survive the build cache (see
the build-classic overlay entry above).

The CI script runs the whole suite serially (`-test.parallel 1`, because a few
tests share and remove the same image tags) with a `-test.skip` denylist. The
matrix runs every Podman version in both modes, and both modes use the same
denylist for that version. The common denylist is 22 tests: the baseline
failures that remain (the three bridge-convert cases, the two relay endpoint
cases and the SELinux bind-mount cases now run), the CLI/engine probes, the
anonymous-volume inspect case and the host-only flakes. Podman 4.3/4.9 add
`TestBuildEscaped` (23 total); Podman 4.3 adds five more (28). The 10 runtime
skips are separate and still happen.
Both e2e workflows build `example-provider` before starting the container, so
the provider cases are exercised. Fixing an engine gap or removing an
environment restriction means removing the test from that list and its note
here.

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
    suite and the e2e matrix below, verifies the overlay commit layout, then
    force-pushes `podman` and tags its `ci(podman):` tip `vX.Y.Z` with an
    annotated tag; there is no release-marker commit. `ci.yml` reuses its
    existing binary and finalize jobs to build the cross-platform binaries,
    named as on docker/compose (`docker-compose-linux-x86_64`, ...), with SBOM
    (`.sbom.json`), SLSA provenance (`.provenance.json`), signature
    (`.sigstore.json`), `.sha256` and a `checksums.txt`, then publishes the
    release, with a Podman note and the upstream release changelog. The
    workflow fetches the upstream tag into `refs/upstream-tags/`, so it never
    touches the fork's release tags or upstream branches, and folds `fixup!`
    commits into their layer during the rebase. A failed rebase or test
    regression leaves `podman` and the releases untouched and files an issue
    instead. Its e2e job builds the provider fixture and runs
    `podman-e2e-container.sh`, the same systemd-in-container harness as the
    manual workflow, so the release gate cannot drift from the suite that was
    validated. The upstream `v*` tag release path in `ci.yml` stays a draft and
    is skipped in the fork. Set the repo to allow GitHub Actions to create and
    dispatch releases.
  - `.github/workflows/podman-e2e.yml` runs the e2e smoke suite on manual
    dispatch against the Podman matrix. The static test binary executes inside
    a privileged container (`debian:12`, `ubuntu:24.04`, `fedora:latest`), so
    old Podman versions need no host compatibility. The Fedora leg uses vfs
    storage and a tmpfs for `/var/tmp`: Fedora 45's buildah rejects an overlay
    over a build context on the container's overlay root. Optional
    `DOCKERHUB_USERNAME` / `DOCKERHUB_TOKEN` secrets raise Docker Hub pull
    limits. Docker is only the outer runtime: it boots systemd and provides
    the bind mount. Each matrix version runs twice: rootless
    (`E2E_ROOTLESS=1`) as a non-root user with subuid/subgid mappings and a
    lingering user systemd manager, matching the local baseline, and rootful
    (`E2E_ROOTLESS=0`) as root. Both legs use the same denylist for that
    version, including the 15 cases that previously failed rootful; only the
    Podman 4.3/4.9 version gaps are skipped in both modes. Switching the outer
    runtime to Podman would not change the inner engine: the user and the
    engine's rootless mode are the parts that matter.

## Testing

- **Unit**: `go test ./pkg/...`; detection and socket probing are
  env/host-based, so `pkg/compose/podman_test.go` needs no API mocks (socket
  tests use a local unix listener), and the build path keeps its existing
  stubs untouched.
- **E2E against Podman**: the workflow builds `bin/build/example-provider`
  (`make example-provider`) before compiling the static test binary, so the
  provider cases have their fixture. The CI script
  `.github/scripts/podman-e2e.sh` builds a static test binary and runs the
  whole suite except the gaps documented above, using a `-test.skip` denylist
  (every test not named there runs), plus a classic-build smoke on every
  Podman version. The workflow boots systemd as PID 1 in the privileged test
  container (with a healthcheck smoke check before the suite), because Podman
  schedules healthchecks through systemd timers. The matrix runs each version
  in both modes: with `E2E_ROOTLESS=1` the suite runs as a non-root user whose
  uid matches the checkout, with subuid/subgid mappings and
  `loginctl enable-linger` for the user manager; rootless storage tries the
  matrix driver and falls back to vfs. With `E2E_ROOTLESS=0` it runs as root
  with the same denylist for that version, so the 15 previously rootful-only
  cases run on current Fedora and the Podman 4.3/4.9 version gaps are skipped
  in both modes. The denylist mirrors the "Known Podman gaps",
  "Environment-dependent failures" and full-suite baseline sections here:
  BuildKit/build-log cases, engine and watch differences, the anonymous-volume
  inspect case and the host-only flakes. The script installs the real Docker CLI (not
  `podman-docker`) so the suite drives Podman's socket the same way a local run
  does; the containerd-store and umask probes then skip instead of failing.
- **Local runs** use the same scripts:
  `CGO_ENABLED=0 make build GO_BUILDTAGS="e2e standalone" && CGO_ENABLED=0 make
  example-provider && CGO_ENABLED=0 go test -c -tags "e2e standalone" -o
  bin/e2e.test ./pkg/e2e` then
  `IMAGE=debian:12 STORAGE_DRIVER=overlay E2E_ROOTLESS=1
  E2E_BIN="$PWD/bin/build/docker-compose" PODMAN_E2E_ALLOW_LOCAL=1
  bash "$PWD/.github/scripts/podman-e2e-container.sh"`.
  `podman-e2e-container.sh` starts the matrix image with systemd as PID 1 and
  runs `podman-e2e.sh` inside it via `docker exec`. Because it is privileged
  and uses `--cgroupns=host` with a writable `/sys/fs/cgroup`, its systemd can
  take over the host console; run it only in a disposable VM. The script
  refuses to start outside CI unless `PODMAN_E2E_ALLOW_LOCAL=1` is set. Notes:
  - set `E2E_ROOTLESS=0` to run the rootful leg locally; it uses the same
    suite and denylist as the rootless leg for that Podman version, so current
    Podman runs the 15 previously rootful-only cases and old versions skip the
    version gaps in both modes;
  - the provider cases need `bin/build/example-provider`; `make
    example-provider` puts it next to the compose binary the tests resolve,
    and `make e2e-compose` / `make e2e-compose-standalone` build it too;
  - the e2e harness copies `$HOME/.docker/config.json` only when it is missing
    (upstream quirk) — the script provides a minimal `{}` file;
  - rootless mode tries the matrix storage driver (overlay with
    `fuse-overlayfs`) and falls back to vfs when nested overlay is unsupported;
  - the bridge transformer runs as container root in rootless mode, so it can
    read the 0600 input and write the output bind mounts. On a
    SELinux-enforcing host a local run still needs a relabel or
    `--security-opt label=disable`; the CI container disables SELinux.
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
- **P5 (implemented)** — restore the health suffix Podman omits from the
  container status so `compose ps` matches Docker and `TestLocalComposeUp`
  passes on Podman 5.8. On 4.3 the suffix is restored too, but the healthcheck
  itself still needs systemd (see known gaps).
- **P6 (implemented)** — run the e2e suite in both rootless and rootful modes
  in CI, with the same denylist for each Podman version. Rootless matches the
  host baseline, and rootful runs the same commit/job-only/idempotent-up/scale/
  interactive-hook cases so a failure is a real engine gap to fix. Podman
  4.3/4.9 version gaps are skipped in both modes, not by mode. The bridge
  transformer runs as container root on rootless engines so it can read the
  0600 input bind mount.

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

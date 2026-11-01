# Podman support — implementation plan

This fork makes `docker/compose` (the reference Compose implementation) run natively
against Podman v4+, with a minimal, rebase-friendly overlay. This document is the
agreed plan of record; the "Overlay index" section doubles as the rebase checklist.

Status (2026-11-01): P0 implemented (detection, socket defaulting, classic-build
forcing) and upstream-release sync automation added; P1+ pending.

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
| 4.3.x (Debian 12) | 1.40 | supported (client floor is exactly 1.40) — see hazard H2 |
| 4.4–4.9 | 1.40/1.41 | supported |
| 5.0–5.7 | 1.41 | supported |
| 5.8+ / 6.x | 1.44 | supported (best parity) |

Local dev machine has Podman 5.8.7 for e2e validation.

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

**Seams (each a few lines, all guarded by the detection):**

| # | File | Change |
|---|---|---|
| S1 | `cmd/main.go` (before `plugin.Run`) | When the Podman profile is on (`COMPOSE_CONTAINER_ENGINE=podman` or argv0 `podman-compose`) and none of `DOCKER_HOST`/`DOCKER_CONTEXT`/`-H`/`--host`/`--context` is set → default `DOCKER_HOST` to `unix://$XDG_RUNTIME_DIR/podman/podman.sock` (rootless) or `unix:///run/podman/podman.sock` (rootful). Explicit `podman` with no socket exits early with an actionable message instead of a dial error. |
| S2 | `pkg/compose/build_bake.go:55` `buildWithBake` | If Podman: force the classic builder (`POST /build?version=1`, `pkg/compose/build_classic.go:199` — Podman serves this via buildah). Needed because `dockerCli.BuildKitEnabled()` assumes any Linux daemon is BuildKit-capable, so a user with buildx installed would get a bake failure (`/grpc`, `/session` don't exist on Podman). Without buildx installed, upstream already falls back cleanly — the guard just makes it deterministic. |
| S3 | `pkg/compose/pull.go:400` `imageDigestResolver` | Podman has no `GET /distribution/{name}/json`. Fallback: resolve the digest from the local `ImageInspect` result, or (better, same effort) query the registry directly over HTTPS using the existing auth provider. Used by `compose config --resolve-image-digests`, `publish`, and dry-run; everything else never calls it. |
| S4 | `pkg/e2e/framework.go:268` `BaseEnvironment` | Also pass through `DOCKER_HOST` (like `DOCKER_CONTEXT` today) so the whole e2e suite can run against a Podman socket. |

Everything else — create/up/wait/exec/logs/cp/kill/stop/pause/secrets/configs
(secrets are injected via `CopyToContainer`, not Swarm APIs — good for Podman) —
runs on endpoints Podman already implements, gated by negotiation.

**Explicitly out of scope on Podman** (document, don't code):
- BuildKit-only build features via bake (multi-arch, build secrets, ssh,
  additional contexts) — classic-builder limitation; upstream already prints a
  clear error (`build_classic.go:233`). Multi-arch via `podman build`+
  `build:` per-platform is a future idea, not v1.
- Swarm-backed features (`deploy:` beyond what compose emulates, external
  overlay networks) — Podman has no Swarm; `isSwarmEnabled` already returns
  false and the existing "network not found" error path is correct.
- `compose alpha` provider/relay and Docker Desktop integrations.

## Known hazards (tracked, with mitigations)

- **H1 — upstream bumps the moby client floor.** Docker CLI 29-era clients
  already reject servers < 1.44 (containers/podman#27890); our pinned
  `moby/moby/client v0.6.2` floor is verified at 1.40. On rebase, if
  `MinAPIVersion` rises above Podman's reported version, pin the previous
  client module in `go.mod` (or carry a one-line patch) and note it here.
- **H2 — Podman 4.3 build endpoint rejects some classic-builder query params**
  (containers/podman#15928, fixed in 4.4). Verify on Debian 12 first; if it
  bites, sanitize the `/build` query (drop empty `cachefrom`, `ulimits=null`,
  etc.) in a small `client.APIClient` decorator following the `DryRunClient`
  pattern — no upstream-file changes needed.
- **H3 — `PUT /containers/{id}/archive?copyUIDGID=true`** (secrets/configs
  injection, `pkg/compose/secrets.go:127`) is a 1.42-era param. Verify on
  Podman 4.3; if ignored, files land with wrong ownership — detect and drop the
  param or fix ownership client-side. Low probability, e2e will tell.
- **H4 — events filter parity** (`compose up --attach`, `watch`, `run`):
  Podman's `GET /events` implements label/event filtering; e2e is the judge.

## Fork & rebase strategy

- Branch model: `main` tracks upstream `docker/compose` tags; `podman` (or
  `podman/main`) is our release branch, rebased onto each upstream tag.
  `upstream` remote, never merge — rebase keeps our hunks visible.
- **All Podman logic lives in new `podman*.go` files**; upstream files get
  only the tiny guarded hunks listed above (S1–S4), each ≤ ~10 lines.
- **Overlay index** (this list IS the rebase checklist — update it when
  touching upstream files): S1 `cmd/main.go`, S2 `build_bake.go`,
  S3 `pull.go`, S4 `pkg/e2e/framework.go`. Rebase recipe: `git fetch upstream
  && git rebase upstream/vX.Y` → expect conflicts only in those four hunks →
  `make build test` + e2e vs Podman → done.
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
- **E2E**: the declarative `Scenario` DSL unchanged; run the suite with
  `DOCKER_HOST=unix:///run/podman/podman.sock` (S4 makes this work) against
  Podman 5.8 locally; add a Debian-12 (Podman 4.3) job before tagging releases
  (podman-in-CI needs a privileged runner or `podman system service` in a
  container — figure out at P2).
- `golangci-lint run --build-tags "e2e" ./...` after every change (AGENTS.md).

## Work plan

- **P0 (implemented)** — detection + S1 + S2 (+ PODMAN.md): `podman compose up`
  works out of the box on 5.8; classic build forced.
- **P1** — S3 digest fallback; e2e suite green against local Podman 5.8
  (fix whatever H2–H4 turn up, decorator pattern if needed).
- **P2** — Debian 12 / Podman 4.3 validation job; CI for the matrix.
- **P3** (optional) — Podman niceties: rootless hints in errors, socket
  auto-activation check, `podman build` shell-out for multi-arch if ever
  needed.

Decision points deferred until e2e evidence exists (don't guess now):
H2/H3 param sanitation, events filter workarounds.

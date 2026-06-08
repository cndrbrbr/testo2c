# Architecture Decisions

## ADR-001 — Scenario `center` must match the unit spawn area, not an arbitrary map focus point

**Date:** 2026-06-08
**Status:** Accepted
**Commit:** `fbdd439` — fix: align scenario map center with actual unit spawn area

### Context

Each scenario profile in `cmd/sim-agent/main.go` (`scenarioMap`) defines both:

- a `center` coordinate, used to position the map view (pushed to all UIs via
  `POST /v1/map/center` in `setupLayers()`, which runs at every agent startup
  — see F-07/F-08 in the README), and
- a `bounds` bbox + per-agent/per-unit offsets, used by `newAgent()` to
  compute each unit's *actual* spawn position.

These two values were configured independently for "central-europe": `center`
was `{51.163375, 10.447683}` (Mühlhausen, Thuringia), while the spawn formula
(`bounds.min + (bounds.max-bounds.min)*0.3 + agent/unit offsets`) resolves to
roughly `{50.15, 9.25}` (Hanau/Gelnhausen, Hesse) — about 150 km away.

### Problem this caused

Because `setupLayers()` re-pushes `center` to `/v1/map/center` on *every* agent
startup, and that endpoint re-centers every connected user's map view, the
mismatch meant tactical symbols rendered entirely outside the default
viewport. This had presumably gone unnoticed because the push only happened
once at initial cluster setup, and operators panned past it manually. Adding
an hourly cluster-restart cron (`restartc.sh`, since the sim-agent containers
hold no persistent volume and re-run `setupLayers()` on every restart) turned
a one-time annoyance into a recurring "I can't see the tactical symbols
anymore" regression — every hour, everyone's view snapped back to the wrong
region.

### Decision

`center` for a scenario MUST be derived from (or kept in sync with) the same
spawn-position formula `newAgent()` uses — not picked independently as an
arbitrary "nice looking" focus point. For "central-europe" we set it to the
mean of the spawn distribution, `{50.15, 9.25}`, and updated `MAP_CENTER` in
`docker-compose.yml` to match (it serves as the orbital nodes' fallback/
default view before any agent has pushed a center).

### Consequences

- When adding a new scenario profile or changing `bounds`/offsets, `center`
  must be recomputed to match — otherwise the same class of bug recurs.
- A more robust long-term fix would be to *compute* `center` from the spawn
  formula at scenario-definition time instead of hardcoding two
  independently-maintained values; this was not done here to keep the change
  minimal and easy to review, but is worth considering if more scenarios are
  added.

---

## ADR-002 — Prefer in-place container recreation over `down`/`up` for config-only changes

**Date:** 2026-06-08
**Status:** Accepted (informal — observed during the ADR-001 fix rollout)

### Context

`restartc.sh` performs a full `docker compose down --remove-orphans` followed
by `up -d`, specifically because a plain compose cycle drops the testo2c
containers from the shared `proxy` Docker network and can leave the shared
`caddy-proxy` with a stale negative DNS cache for the old container
names/IPs (see the comment block at the top of `restartc.sh`).

### Decision

For changes that only require recreating testo2c's *own* containers (e.g.
picking up a new image build or changed environment variables), use:

```
docker compose -f docker-compose.yml -f docker-compose.caddy-external.yml \
  up -d --no-deps <service> [<service> ...]
```

This recreates only the named services in place, without ever detaching them
from the `proxy` network — avoiding the DNS-cache problem `restartc.sh` exists
to work around, and avoiding the disruptive full-stack bounce (which, per
ADR-001, was itself the proximate cause of the symbol-visibility regression).

### Consequences

`restartc.sh` should be reserved for situations that genuinely require a full
`down`/`up` cycle — e.g. recovering from a stuck network attachment. Routine
image/config updates should use the targeted `up -d --no-deps` form instead.

**Update — 2026-06-08:** the hourly cron invoking `restartc.sh` (`0 * * * *
/root/testo2c/restartc.sh`) has been removed. It was running the full
down/up cycle on a fixed schedule "just in case", but per ADR-001 this
re-triggered each sim-agent's startup routine — including the map-center
push — every hour, turning a one-time setup quirk into a recurring
symbol-visibility regression. With ADR-001 fixed and the cluster's own
`restart: unless-stopped` + healthchecks handling crash recovery,
`restartc.sh` is no longer needed on a schedule; run it manually only if the
cluster is observed to have actually wedged itself off the proxy network.

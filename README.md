# testo2c — OrbitalC2Core Simulation Harness

Simulation and integration-test harness for [OrbitalC2Core](https://github.com/cndrbrbr/orbitalc2core).

Spins up a 3-node OrbitalC2Core cluster and attaches one simulation agent per node. Each agent generates randomised NATO ADATP-3 messages and injects them into the **other two** nodes, creating a continuously evolving tactical picture across all three map displays.

---

## Feature List

### F-01 — Three-Node OrbitalC2Core Cluster

`docker-compose.yml` brings up the full cluster with a single command, zero manual configuration:

| Service | Host port | Role |
|---------|-----------|------|
| `orbital-node1` | `:8081` | OrbitalC2Core node 1 (UI + REST API) |
| `orbital-node2` | `:8082` | OrbitalC2Core node 2 |
| `orbital-node3` | `:8083` | OrbitalC2Core node 3 |
| `adatp3-adapter-1` | `:9181` | ADATP-3 adapter → node 1 |
| `adatp3-adapter-2` | `:9182` | ADATP-3 adapter → node 2 |
| `adatp3-adapter-3` | `:9183` | ADATP-3 adapter → node 3 |
| `sim-agent-1` | `:9201` | Simulation agent 1 (control API) |
| `sim-agent-2` | `:9202` | Simulation agent 2 |
| `sim-agent-3` | `:9203` | Simulation agent 3 |
| `nats` | `:4222` | NATS JetStream (inter-node sync) |

- Node IDs are hardcoded deterministic UUIDs — no `.env` setup required for development.
- All three OrbitalC2Core UIs are accessible on the host immediately after `docker compose up`.
- NATS JetStream synchronises all three nodes so changes from any node propagate to the others.
- The scenario map center is pushed to all nodes at startup so all UIs open at the right area.

---

### F-02 — Simulation Agent Container per Node

Each `sim-agent-{n}` is a standalone Go binary in its own lightweight container (`Dockerfile.sim-agent`), built with `CGO_ENABLED=0`.

Configuration via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENT_ID` | — | Agent identity: `1`, `2`, or `3` |
| `OWN_ORBITAL_URL` | — | Base URL of this agent's own orbital-node |
| `PEER_ADATP3_URLS` | — | Comma-separated ADATP-3 adapter URLs of the other two nodes |
| `SCENARIO` | `central-europe` | Scenario profile (see F-07) |
| `SIM_INTERVAL` | `10` | Seconds between simulation cycles |
| `SIM_BURST` | `10` | Number of ADATP-3 messages generated per cycle |
| `SIM_AUTOSTART` | `true` | Start the loop automatically on container start |
| `SIM_LISTEN` | `:9200` | Control API listen address |
| `STARTUP_TIMEOUT` | `60` | Seconds to wait for dependencies before aborting |

---

### F-03 — ADATP-3 Message Generator

Each cycle generates `SIM_BURST` (default 10) ADATP-3 text messages, distributed as:

| Count | Type | Effect on map |
|-------|------|---------------|
| 3 | `OWNSITREP` | Moving friendly force elements |
| 2 | `SITREP` | Situation narrative reports |
| 2 | `SPOTREP` (enemy) | Red contacts at random positions |
| 1 | `SPOTREP` (unknown) | Unknown contact symbol |
| 1 | `LOGREP` | Logistics state update on a friendly unit |
| 1 | `ORBAT` | Unit hierarchy (emitted every 5th cycle, otherwise replaced by extra SITREP) |

All generated messages are valid ADATP-3 text in the format accepted by `POST /adatp3/message`. Positions are expressed as WGS84 coordinates. DTGs are set to the current UTC time.

Generated ADATP-3 output is also written to the agent's structured log so messages can be replayed independently.

---

### F-04 — Cross-Node Message Delivery

Each agent sends its generated messages to **both peer nodes' ADATP-3 adapters**, not to its own node:

```
sim-agent-1  →  adatp3-adapter-2  →  orbital-node2
             →  adatp3-adapter-3  →  orbital-node3

sim-agent-2  →  adatp3-adapter-1  →  orbital-node1
             →  adatp3-adapter-3  →  orbital-node3

sim-agent-3  →  adatp3-adapter-1  →  orbital-node1
             →  adatp3-adapter-2  →  orbital-node2
```

Delivery uses `POST /adatp3/message` with a JSON envelope (`{"messages": [...]}`), sending all cycle messages in one request per peer. On failure, the agent retries 3 times with exponential backoff (1 s, 2 s, 4 s). Delivery results are logged per peer per cycle.

---

### F-05 — Unit Movement Simulation

Each agent owns 3 "moving units" (9 units total across the cluster). Between cycles each unit advances along a randomly chosen bearing by a randomly chosen distance (100 m – 2 km). This produces natural-looking patrol and movement patterns on the map.

- Positions are clamped to the scenario bounding box so units never wander off-map.
- Each unit's OWNSITREP `STRENGTH` and `STATUS` fields also evolve slowly across cycles to reflect attrition and recovery.
- Unit names and echelons are drawn from the scenario profile (see F-07) and remain consistent across cycles so force elements update in place rather than accumulating duplicates.

---

### F-06 — Direct Orbital API Control

Agents use the `orbitalc2core/remotecontrol/client` Go package directly for operations that go beyond ADATP-3 injection:

| Action | When | API call |
|--------|------|----------|
| Create simulation layer | Startup | `POST /v1/layers` — one named `"Sim-Agent-N"` layer per agent |
| Set map center | Startup | `POST /v1/map/center` — positions map at scenario area |
| Clean up features | `/sim/reset` | `DELETE /v1/features/{id}` for all agent-created features |
| Read COP snapshot | `/sim/status` | `GET /api/v1/common-operational-picture` — counts entities |

---

### F-07 — Scenario Profiles

Built-in scenarios selected via the `SCENARIO` environment variable:

| ID | Area | Center | Bounding box | Character |
|----|------|--------|-------------|-----------|
| `central-europe` | Germany | 51.16°N 10.45°E | 47–55°N, 6–15°E | Mixed land; Heer units, IFV/HMMWV platforms |
| `north-sea` | Helgoland/Kiel | 54.18°N 7.89°E | 53–56°N, 7–12°E | Littoral; Marine and coastal units |
| `baltic` | Baltic Sea | 56.10°N 20.00°E | 54–58°N, 15–25°E | Land/sea; mixed NATO and OPFOR units |
| `alpine` | Austrian Alps | 47.20°N 12.00°E | 46–48°N, 9–15°E | Mountain; reduced movement speed |

Each profile defines: bounding box, map center, unit name prefixes, echelon set, side distribution (blue/red split), and maximum movement speed per cycle.

---

### F-08 — Simulation Control REST API

Each agent exposes a lightweight HTTP API on `SIM_LISTEN` (default `:9200`):

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/sim/status` | JSON: running state, current cycle, entity counts via COP, last error, per-peer delivery stats |
| `POST` | `/sim/start` | Start (or resume) the simulation loop |
| `POST` | `/sim/stop` | Pause after the current cycle completes |
| `POST` | `/sim/step` | Run exactly one cycle immediately (regardless of `SIM_AUTOSTART`) |
| `POST` | `/sim/reset` | Stop loop, delete all agent-created features from own node, reset unit positions to scenario start |
| `GET` | `/sim/log` | Last 100 structured log entries as a JSON array |

---

### F-09 — Structured JSON Logging

Every significant event is emitted as a JSON log line to stdout:

```json
{"time":"2026-04-26T10:00:05Z","agent":1,"event":"cycle_start","cycle":7,"scenario":"central-europe"}
{"time":"2026-04-26T10:00:05Z","agent":1,"event":"message_generated","type":"OWNSITREP","serial":"007-1","unit":"1PzGrenBtl212","lat":51.234,"lon":9.876}
{"time":"2026-04-26T10:00:06Z","agent":1,"event":"delivery","peer":"adatp3-adapter-2","messages":10,"ok":true,"elapsed_ms":87}
{"time":"2026-04-26T10:00:06Z","agent":1,"event":"delivery","peer":"adatp3-adapter-3","messages":10,"ok":false,"attempt":1,"error":"connection refused"}
{"time":"2026-04-26T10:00:08Z","agent":1,"event":"delivery","peer":"adatp3-adapter-3","messages":10,"ok":true,"attempt":2,"elapsed_ms":134}
{"time":"2026-04-26T10:00:08Z","agent":1,"event":"cycle_done","cycle":7,"total_delivered":20,"elapsed_ms":3021}
```

All log entries are also held in a ring buffer (last 100) and served via `GET /sim/log`.

---

### F-10 — Health-Aware Startup

The agent does not start the simulation loop until all dependencies are healthy:

1. Polls `GET /healthz` on its own orbital-node every 2 s until status is `ok`.
2. Polls `GET /health` on each peer ADATP-3 adapter every 2 s until all respond `ok`.
3. If any dependency does not become healthy within `STARTUP_TIMEOUT` (default 60 s), the agent exits with code 1 and Docker Compose restarts it.

This ensures the cluster is fully ready before the first message is injected.

---

## Quick Start

```bash
# Clone both repos side-by-side (orbitalc2core must be a sibling directory
# because testo2c builds the OrbitalC2Core images from source)
git clone git@github.com:cndrbrbr/orbitalc2core.git
git clone git@github.com:cndrbrbr/testo2c.git

cd testo2c
docker compose up --build

# Node UIs
open http://localhost:8081   # node 1
open http://localhost:8082   # node 2
open http://localhost:8083   # node 3

# Agent control APIs
curl http://localhost:9201/sim/status   # agent 1 status
curl -X POST http://localhost:9201/sim/stop   # pause agent 1
curl -X POST http://localhost:9201/sim/step   # run one cycle manually
curl -X POST http://localhost:9201/sim/reset  # clear and restart
```

Change the scenario:

```bash
# Edit docker-compose.yml: set SCENARIO=north-sea on all sim-agent services
docker compose up --build
```

---

## Project Structure (planned)

```
testo2c/
├── cmd/
│   └── sim-agent/          # Simulation agent binary entry point
│       └── main.go
├── internal/
│   ├── agent/              # Core simulation loop, state, and control API
│   │   └── agent.go
│   ├── generator/          # ADATP-3 message generator
│   │   └── generator.go
│   └── scenario/           # Scenario profile definitions
│       └── scenario.go
├── deploy/
│   └── Dockerfile.sim-agent
├── docker-compose.yml
└── README.md
```

The simulation agent imports `orbitalc2core/remotecontrol/client` and `orbitalc2core/messages/adatp3` as Go module dependencies; the `go.mod` references the sibling directory via a `replace` directive for local development and the GitHub URL for production builds.

---

## Dependencies

| Component | Source |
|-----------|--------|
| OrbitalC2Core node image | Built from `../orbitalc2core` (local) or `github.com/cndrbrbr/orbitalc2core` |
| ADATP-3 adapter image | Built from `../orbitalc2core/deploy/Dockerfile.adatp3` |
| NATS | `nats:2-alpine` (Docker Hub) |
| Go | 1.22+ (agent only, `CGO_ENABLED=0`) |

---

## License

Work in progress — license TBD.

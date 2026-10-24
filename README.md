# Fog Play

Control plane for Fog Play: people share their gaming PCs (hosts), and players
stream games from the nearest idle one. This service handles accounts, the host
registry, live host presence, and the game catalog. Video never passes through
it; it flows peer to peer between player and host.

Design doc: https://claude.ai/artifact/WL4ZkM1vrRcZcwPz1M3Zct

**Try it on real PCs:** [docs/real-pc-test.md](docs/real-pc-test.md).
Streaming uses [Sunshine](https://github.com/LizardByte/Sunshine) on the host
and [Moonlight](https://moonlight-stream.org) on the player for now.

There are two binaries, both command-line only:

| Binary | Runs on | Does |
| --- | --- | --- |
| `fogplay-server` | a server | The control plane API |
| `fogplay` | any PC | `fogplay play GAME` streams a game from another PC with Moonlight (`register`, `login`, `games` too). `fogplay host` shares this PC through Sunshine: registers it, sends heartbeats, accepts bookings, pairs the player's Moonlight, and unpairs it after the session. |

## Run it

```sh
make up          # Postgres, Redis and the API on :8080
```

Or run the API from source against containerised dependencies:

```sh
make deps
DATABASE_URL=postgres://fogplay:fogplay@localhost:5432/fogplay?sslmode=disable \
JWT_SECRET=dev-only-secret-change-me-0123456789 \
go run ./cmd/fogplay-server
```

| Variable | Default | Notes |
| --- | --- | --- |
| `DATABASE_URL` | (required) | Postgres; migrations run on startup |
| `JWT_SECRET` | (required) | At least 32 bytes |
| `REDIS_URL` | `redis://localhost:6379/0` | Host presence |
| `FOGPLAY_ADDR` | `:8080` | Listen address |
| `HEARTBEAT_TTL` | `30s` | A host counts as offline this long after its last heartbeat |
| `SESSION_START_TIMEOUT` | `60s` | A matched session that has not gone active by then fails and frees its host |

## Test

```sh
make deps && make test
```

API tests run against real Postgres and Redis and wipe both test databases.

## API (v1)

All endpoints except register and login take `Authorization: Bearer <token>`.

| Method | Path | Who | Purpose |
| --- | --- | --- | --- |
| POST | `/v1/auth/register` | anyone | `{email, password, role}`; role is `player`, `host`, or `both` |
| POST | `/v1/auth/login` | anyone | Returns a token |
| GET | `/v1/me` | any user | Current user |
| GET | `/v1/games` | any user | Catalog with `idle_hosts` and `online_hosts` per game |
| POST | `/v1/hosts` | host | Register a PC: `{name, region, gpu, cpu, games: [{slug, title}]}` |
| GET | `/v1/hosts` | host | Your PCs |
| GET | `/v1/hosts/{id}` | host | One of your PCs |
| PUT | `/v1/hosts/{id}/games` | host | Replace installed games |
| PUT | `/v1/hosts/{id}/status` | `fogplay host` | Heartbeat: `{status: idle \| busy \| offline}` |
| GET | `/v1/hosts/{id}/session` | `fogplay host` | The live session booked on this PC, or 204 |
| POST | `/v1/sessions` | any user | `{game, latency_ms: {region: ms}}`; books the best idle host |
| GET | `/v1/sessions` | any user | Your last 50 sessions as a player |
| GET | `/v1/sessions/{id}` | player, host owner | One session |
| PUT | `/v1/sessions/{id}/state` | player, host owner | `{state, reason}`: `connecting`, `active`, `ended`, `failed` |
| DELETE | `/v1/sessions/{id}` | player, host owner | End the session |
| GET (WebSocket) | `/v1/sessions/{id}/signal` | player, host owner | Relay messages to the other side of a live session |

### Sessions

```
matched ──▶ connecting ──▶ active ──▶ ended
   └────────────┴─────────────┴─────▶ failed / ended
```

Matching picks, among idle hosts that have the game and are not yours, the
one in the region with the lowest `latency_ms` the client reported, then the
most recent heartbeat. Postgres partial unique indexes guarantee a host and a
player each have at most one live session, so concurrent requests can't
double-book a PC; the loser moves on to the next candidate. Ending an active
session records billable `minutes`, rounded up.

```sh
TOKEN=$(curl -s localhost:8080/v1/auth/register -d '{"email":"me@example.com","password":"password1","role":"both"}' | jq -r .token)
HOST=$(curl -s localhost:8080/v1/hosts -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"Desk PC","region":"eu-central","gpu":"RTX 4080","games":[{"slug":"dota-2","title":"Dota 2"}]}' | jq -r .id)
curl -s -X PUT localhost:8080/v1/hosts/$HOST/status -H "Authorization: Bearer $TOKEN" -d '{"status":"idle"}'
curl -s localhost:8080/v1/games -H "Authorization: Bearer $TOKEN"
```

### Signaling

`/v1/sessions/{id}/signal` relays JSON messages `{type, data}` between the
session's player and host; the server adds `from`. Messages sent before the
other side connects are queued. The relay closes when the session ends. The
hub is in memory, so run a single server instance for now.

The Moonlight flow uses three messages: the host sends `host-info` (address,
app), the player sends `pair-pin` (a random PIN Moonlight pairs with), and the
host answers `pair-result` once Sunshine has accepted it.

## Layout

```
cmd/fogplay-server server entrypoint
cmd/fogplay        client: play and host subcommands
internal/api       HTTP handlers and auth middleware
internal/signal    WebSocket relay hub
internal/client    Go API client
internal/agent     host logic ("fogplay host") and Sunshine API client
internal/player    player session flow and Moonlight runner
internal/e2e       full-flow tests with fake Sunshine and Moonlight
internal/auth      password hashing, JWT
internal/store     Postgres queries
internal/presence  Redis heartbeats with TTL
internal/match     host ranking
internal/db        pool and embedded SQL migrations
```

## Roadmap

1. ~~Skeleton~~ and ~~accounts, hosts, heartbeats, catalog~~
2. ~~Sessions and matching: session API, state machine, latency-based host pick~~
3. ~~Signaling relay, host and player client with Sunshine + Moonlight~~
4. Own WebRTC streaming in the client, coturn for NAT traversal
5. Metering, then payments and host payouts

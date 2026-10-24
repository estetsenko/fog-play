# Fog Play

Control plane for Fog Play: people share their gaming PCs (hosts), and players
stream games from the nearest idle one. This service handles accounts, the host
registry, live host presence, and the game catalog. Video never passes through
it; it flows peer to peer between player and host.

Design doc: https://claude.ai/artifact/WL4ZkM1vrRcZcwPz1M3Zct

**Try it on real PCs:** [docs/real-pc-test.md](docs/real-pc-test.md).
Streaming is our own engine: the host captures and encodes its screen with
FFmpeg (downloaded on first use on Windows) and sends it over WebRTC; the player
watches in a browser and its mouse and keyboard go back over a data channel.
Nothing else to install ([third-party notes](docs/third-party.md)).

There are two binaries, both command-line only:

| Binary | Runs on | Does |
| --- | --- | --- |
| `fogplay-server` | a server | The control plane API and the browser play page (`/play`) |
| `fogplay` | any PC | `fogplay play GAME` books a host and opens the stream in the browser (`register`, `login`, `games` too). `fogplay host` shares this PC: registers it, sends heartbeats, accepts bookings, starts the game, streams the screen, and replays the player's input. |

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

The WebRTC handshake uses three messages: the player's page sends `hello`, the
host answers with an `offer` (SDP with all ICE candidates), and the page replies
with an `answer`. A reloaded page says hello again and gets a fresh offer.
Browsers can't set headers on a WebSocket, so this route also takes the token
as `?access_token=`.

## Layout

```
cmd/fogplay-server server entrypoint
cmd/fogplay        client: play and host subcommands
internal/api       HTTP handlers and auth middleware
internal/signal    WebSocket relay hub
internal/client    Go API client
internal/api/web   browser play page (embedded)
internal/agent     host logic ("fogplay host"): bookings, games, session lifecycle
internal/stream    streaming engine: FFmpeg capture/encode, Pion WebRTC, input
internal/fetch     downloads FFmpeg release builds
internal/player    player session flow (book, open browser, wait)
internal/e2e       full-flow tests, including FFmpeg to a Go WebRTC peer
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
4. ~~fogplay downloads and runs Sunshine and Moonlight itself~~
5. Own streaming engine ([plan](https://claude.ai/artifact/9YAVXhfR5UFrqscCoS7GRQ)):
   ~~phase 1, video and mouse/keyboard in the browser on LAN~~; phase 2 audio,
   gamepad, game-only sessions; phase 3 coturn for play across NAT; phase 4 quality
6. Metering, then payments and host payouts

# Fog Play

Control plane for Fog Play: people share their gaming PCs (hosts), and players
stream games from the nearest idle one. This service handles accounts, the host
registry, live host presence, and the game catalog. Video never passes through
it; it flows peer to peer between player and host.

Design doc: https://claude.ai/artifact/WL4ZkM1vrRcZcwPz1M3Zct

## Run it

```sh
make up          # Postgres, Redis and the API on :8080
```

Or run the API from source against containerised dependencies:

```sh
make deps
DATABASE_URL=postgres://fogplay:fogplay@localhost:5432/fogplay?sslmode=disable \
JWT_SECRET=dev-only-secret-change-me-0123456789 \
go run ./cmd/fogplay
```

| Variable | Default | Notes |
| --- | --- | --- |
| `DATABASE_URL` | (required) | Postgres; migrations run on startup |
| `JWT_SECRET` | (required) | At least 32 bytes |
| `REDIS_URL` | `redis://localhost:6379/0` | Host presence |
| `FOGPLAY_ADDR` | `:8080` | Listen address |
| `HEARTBEAT_TTL` | `30s` | A host counts as offline this long after its last heartbeat |

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
| PUT | `/v1/hosts/{id}/status` | host agent | Heartbeat: `{status: idle \| busy \| offline}` |

```sh
TOKEN=$(curl -s localhost:8080/v1/auth/register -d '{"email":"me@example.com","password":"password1","role":"both"}' | jq -r .token)
HOST=$(curl -s localhost:8080/v1/hosts -H "Authorization: Bearer $TOKEN" \
  -d '{"name":"Desk PC","region":"eu-central","gpu":"RTX 4080","games":[{"slug":"dota-2","title":"Dota 2"}]}' | jq -r .id)
curl -s -X PUT localhost:8080/v1/hosts/$HOST/status -H "Authorization: Bearer $TOKEN" -d '{"status":"idle"}'
curl -s localhost:8080/v1/games -H "Authorization: Bearer $TOKEN"
```

## Layout

```
cmd/fogplay        entrypoint
internal/api       HTTP handlers and auth middleware
internal/auth      password hashing, JWT
internal/store     Postgres queries
internal/presence  Redis heartbeats with TTL
internal/db        pool and embedded SQL migrations
```

## Roadmap

1. ~~Skeleton~~ and ~~accounts, hosts, heartbeats, catalog~~ (this PR)
2. Sessions and matching: session API, state machine, latency-based host pick
3. Signaling: WebSocket relay for SDP and ICE, coturn
4. Host agent: Go daemon that wraps Sunshine
5. Metering, then payments and host payouts

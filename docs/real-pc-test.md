# Test Fog Play on real PCs

This walks through one full session: a player PC books a host PC, pairs
Moonlight automatically, streams a game, and ends the session. It takes
about 20 minutes the first time.

You need three roles. They can be two machines: run the server on the
host PC or on the player PC.

| Role | Runs | Needs |
| --- | --- | --- |
| Server | `fogplay-server`, Postgres, Redis | Docker; reachable from both PCs on port 8080 |
| Host PC | Sunshine + `fogplay-agent` | Windows or Linux with a GPU (macOS works as a host only with limited Sunshine support) |
| Player PC | Moonlight + `fogplay` | Windows, macOS or Linux |

For the first test, put both PCs on the same LAN. Moonlight connects straight
to the host, so across the internet you need a VPN such as Tailscale or
forwarding Sunshine's ports (see the Sunshine docs for the list).

## 1. Download the binaries

Take the zip for each PC's OS from the latest GitHub release of this repo
(or from the `fogplay-binaries` artifact of the latest `release` workflow run on main).
Each zip has `fogplay` (player) and `fogplay-agent` (host).

Or build from source with Go 1.26: `make build` puts all three binaries in `bin/`.

## 2. Start the server

On the machine that will run it, from a clone of this repo:

```sh
make up
```

Check it from **both** PCs, replacing the address with that machine's LAN IP:

```sh
curl http://192.168.1.20:8080/readyz   # 200 and empty body means ready
```

If the other PC can't reach it, allow port 8080 in that machine's firewall.

## 3. Set up the host PC

1. Install [Sunshine](https://github.com/LizardByte/Sunshine/releases) and open
   its web UI at https://localhost:47990. Set a username and password.
2. In Sunshine, open **Applications** and add the games you want to offer. Each
   app name becomes a game in the catalog: "Dota 2" becomes `dota-2`. The
   default "Desktop" app works for a first test.
3. Create the agent config. The default location is
   `%APPDATA%\fogplay\agent.json` on Windows, `~/.config/fogplay/agent.json` on Linux,
   and `~/Library/Application Support/fogplay/agent.json` on macOS. You can also pass `--config PATH`.

   ```json
   {
     "server": "http://192.168.1.20:8080",
     "email": "host@example.com",
     "password": "pick-a-password",
     "name": "Living room PC",
     "region": "home",
     "gpu": "RTX 4070",
     "sunshine_url": "https://localhost:47990",
     "sunshine_user": "the Sunshine username",
     "sunshine_password": "the Sunshine password"
   }
   ```

   `address` is optional. The agent advertises the PC's LAN IP; set it if the
   player should use another address, such as a Tailscale IP.
4. Run the agent and leave it running:

   ```sh
   fogplay-agent
   ```

   On first run it creates the host account and registers the PC. You should see
   `games synced count=N` and `online, waiting for players`.

## 4. Play from the player PC

1. Install [Moonlight](https://moonlight-stream.org). `fogplay` finds it in its
   standard install location; otherwise pass `--moonlight PATH`.
2. Create a player account. This must be a different account from the host's,
   because you can't book your own PC.

   ```sh
   fogplay register --server http://192.168.1.20:8080 --email player@example.com
   ```
3. See what's available, then play:

   ```sh
   fogplay games
   fogplay play desktop
   ```

What should happen:

1. `host booked`, then `host ready` with the host's address.
2. Moonlight pairs on its own. You don't enter a PIN; the agent enters it into
   Sunshine for you. The agent logs `Moonlight paired`.
3. The stream window opens with the game.
4. Close Moonlight or press Ctrl+C. You'll see `Session ended.`, and the agent logs
   `session over`, unpairs Moonlight from Sunshine, and goes back to waiting.

Run `fogplay games` again: the host shows as free.

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| `no free host has this game right now` | The agent isn't running, the game slug is wrong (check `fogplay games`), or you're signed in as the host's own account |
| Agent: `wrong username or password` for Sunshine | `sunshine_user` / `sunshine_password` don't match the Sunshine web UI login |
| Agent: `cannot read Sunshine apps` | Sunshine isn't running or `sunshine_url` is wrong; the agent falls back to the `games` list in its config |
| `waiting for the host to answer` times out | The agent can't reach the server, or is still busy with a previous session |
| `moonlight pair` fails | The player PC can't reach the host's address. Allow Sunshine through the host's firewall. |
| Pairing times out in the agent | Moonlight never reached Sunshine; same network checks. Run the agent with `--debug` to see each PIN attempt. |
| Session fails after 60 s with `timed out before streaming started` | Pairing or connection took too long; see the rows above |

Sunshine versions from 2026 on list pending pairings by id, and older ones
don't. The agent handles both.

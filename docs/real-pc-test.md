# Test Fog Play on real PCs

This walks through one full session: a player PC books a host PC, pairs
automatically, streams a game, and ends the session. It takes about 15
minutes the first time.

You don't install any streaming software. `fogplay host` downloads and runs a
private copy of [Sunshine](https://github.com/LizardByte/Sunshine), and
`fogplay play` downloads a portable [Moonlight](https://moonlight-stream.org)
unless one is already installed. Both are fetched from their official GitHub
releases on first use and cached (see [third-party.md](third-party.md)).

You need three roles. They can be two machines: run the server on the
host PC or on the player PC.

| Role | Runs | Needs |
| --- | --- | --- |
| Server | `fogplay-server`, Postgres, Redis | Docker; reachable from both PCs on port 8080 |
| Host PC | `fogplay host` | Windows or Linux (x86_64) with a GPU |
| Player PC | `fogplay play` | Windows, macOS or Linux (x86_64) |

For the first test, put both PCs on the same LAN. Moonlight connects straight
to the host, so across the internet you need a VPN such as Tailscale or
forwarding Sunshine's ports (see the Sunshine docs for the list).

## 1. Download the binaries

Take the zip for each PC's OS from the latest GitHub release of this repo
(or from the `fogplay-binaries` artifact of the latest `release` workflow run on main).
Each zip has the `fogplay` client, which both PCs use: `fogplay host` on the
host and `fogplay play` on the player.

Or build from source with Go 1.26: `make build` puts `fogplay` and
`fogplay-server` in `bin/`.

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

1. Create the host config. The default location is
   `%APPDATA%\fogplay\host.json` on Windows, `~/.config/fogplay/host.json` on Linux,
   and `~/Library/Application Support/fogplay/host.json` on macOS. You can also pass `--config PATH`.

   ```json
   {
     "server": "http://192.168.1.20:8080",
     "email": "host@example.com",
     "password": "pick-a-password",
     "name": "Living room PC",
     "gpu": "RTX 4070"
   }
   ```

   The host always offers its desktop as the game `desktop`, which is enough
   for a first test. To offer games too, add them before the first run:
   `"apps": [{"name": "Dota 2", "cmd": "C:\\Path\\To\\dota2.exe"}]`. Each
   name becomes a catalog slug ("Dota 2" becomes `dota-2`).

   `address` is optional. The host advertises the PC's LAN IP; set it if the
   player should use another address, such as a Tailscale IP.
2. Go online and leave it running:

   ```sh
   fogplay host
   ```

   The first run downloads Sunshine (about 100 MB), creates the host account
   and registers the PC. You should see `Sunshine running`, `games synced count=N`
   and `online, waiting for players`. If Windows asks to allow network access
   for Sunshine, allow it on private networks.

## 4. Play from the player PC

1. Create a player account. This must be a different account from the host's,
   because you can't book your own PC.

   ```sh
   fogplay register --server http://192.168.1.20:8080 --email player@example.com
   ```
2. See what's available, then play:

   ```sh
   fogplay games
   fogplay play desktop
   ```

What should happen:

1. The first time only: `downloading Moonlight (one time)`.
2. `host booked`, then `host ready` with the host's address.
3. Moonlight pairs on its own. You don't enter a PIN; the host enters it into
   Sunshine for you. The host logs `Moonlight paired`.
4. The stream window opens with the game.
5. Close Moonlight or press Ctrl+C. You'll see `Session ended.`, and the host logs
   `session over`, unpairs Moonlight from Sunshine, and goes back to waiting.

Run `fogplay games` again: the host shows as free.

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| `no free host has this game right now` | The host isn't running, the game slug is wrong (check `fogplay games`), or you're signed in as the host's own account |
| Host: `another Sunshine is already running` | Quit the Sunshine you installed yourself (or its Windows service), or tell fogplay to use it by setting `sunshine_user` and `sunshine_password` in host.json |
| Host: `Sunshine exited at startup` / `did not start` | Open the `sunshine.log` path it prints. On Linux, Sunshine needs a desktop session (X11 or Wayland) and access to `/dev/uinput` for input |
| Host or player: download fails | The PC can't reach github.com; download Sunshine or Moonlight yourself and use `sunshine_user`/`sunshine_password` or `--moonlight PATH` |
| `waiting for the host to answer` times out | The host can't reach the server, or is still busy with a previous session |
| `moonlight pair` fails | The player PC can't reach the host's address. Allow Sunshine through the host's firewall. |
| Pairing times out in the host | Moonlight never reached Sunshine; same network checks. Run the host with `--debug` to see each PIN attempt. |
| Session fails after 60 s with `timed out before streaming started` | Pairing or connection took too long; see the rows above |

Sunshine versions from 2026 on list pending pairings by id, and older ones
don't. The host handles both.

### Using your own Sunshine or Moonlight

If Sunshine is already installed on the host, add its web UI login to host.json
(`"sunshine_user"`, `"sunshine_password"`, and `"sunshine_url"` if it isn't
https://localhost:47990). fogplay then uses it instead of its own copy, and
offers the apps configured in it. If Moonlight is installed on the player in its
standard location, `fogplay play` uses it; `--moonlight PATH` picks another one.

Downloaded copies live in the user cache folder (`%LOCALAPPDATA%\fogplay` on
Windows, `~/.cache/fogplay` on Linux, `~/Library/Caches/fogplay` on macOS).
Delete it to force a fresh download.

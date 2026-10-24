# Test Fog Play on real PCs

This walks through one full session: a player PC books a host PC, watches and
controls it in a browser, and ends the session. It takes about 10 minutes the
first time.

Streaming is Fog Play's own engine. `fogplay host` captures the screen with
FFmpeg, encodes it on the GPU, and sends it over WebRTC. The player needs only a
browser. On Windows, FFmpeg is downloaded on first use (see
[third-party.md](third-party.md)).

You need three roles. They can be two machines: run the server on the
host PC or on the player PC.

| Role | Runs | Needs |
| --- | --- | --- |
| Server | `fogplay-server`, Postgres, Redis | Docker; reachable from both PCs on port 8080 |
| Host PC | `fogplay host` | Windows 10 or 11 (x86_64) with an NVIDIA, AMD or Intel GPU |
| Player PC | `fogplay play` and a browser | Chrome or Edge (Firefox works but has more latency) |

This phase is for a LAN: the player connects straight to the host. Play across
the internet (through a relay) is phase 3. A VPN such as Tailscale works
meanwhile.

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
The player's browser loads the play page from this server too.

## 3. Set up the host PC

1. Create the host config. The default location is
   `%APPDATA%\fogplay\host.json` on Windows, `~/.config/fogplay/host.json` on Linux.
   You can also pass `--config PATH`.

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
   for a first test. To offer games too, add
   `"apps": [{"name": "Dota 2", "cmd": "C:\\Path\\To\\dota2.exe"}]`. Each
   name becomes a catalog slug ("Dota 2" becomes `dota-2`), and the game is
   started when a session for it begins and closed when it ends.

   Optional stream settings: `"bitrate_mbps"` (default 15), `"fps"` (default
   60), `"encoder"` (`h264_nvenc`, `h264_amf`, `h264_qsv` or `libx264`; default:
   the first that works), `"ffmpeg"` (path to your own ffmpeg.exe).
2. Go online and leave it running:

   ```sh
   fogplay host
   ```

   The first run downloads FFmpeg (about 150 MB), creates the host account
   and registers the PC. You should see `stream ready ... encoder=h264_nvenc`
   (or amf/qsv for AMD/Intel), `games synced count=N` and
   `online, waiting for players`.

   When the first player connects, Windows asks whether to allow fogplay on
   networks. **Allow it on private networks**, or the video can't get through.

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

1. `host booked`, and your browser opens the play page. (On a PC without a
   browser, `fogplay play desktop --no-browser` prints the link to open elsewhere.)
2. The page says `Connecting to host…`, then `Playing`, and the host's screen
   appears. The host logs `player connected, starting stream`.
3. The line at the top shows resolution, frame rate, bitrate, round trip and
   buffer delay. Please send me that line from your test.
4. Move the mouse and type over the video; it controls the host. For games,
   press **Game mode**: fullscreen, with the mouse captured. Press Esc to get
   the mouse back.
5. Press **End session**. The page says `Session ended`, `fogplay play` prints
   `Session ended.`, and the host logs `session over`. Ctrl+C in the terminal
   ends it too.

Reloading the page during a session reconnects to the same stream.

Run `fogplay games` again: the host shows as free.

## Troubleshooting

| Symptom | Likely cause |
| --- | --- |
| `no free host has this game right now` | The host isn't running, the game slug is wrong (check `fogplay games`), or you're signed in as the host's own account |
| Host: `no H.264 encoder works with this FFmpeg` | GPU drivers are missing or old. Update them, or set `"encoder": "libx264"` to encode on the CPU |
| Host: download fails | The PC can't reach github.com. Download an FFmpeg build yourself and set `"ffmpeg"` to its ffmpeg.exe |
| Page stays on `Connecting to host…` | Windows blocked fogplay: allow it in Windows Defender Firewall for private networks, then reload the page. Also check that both PCs are on the same network |
| Session fails with `stream failed on the host: ffmpeg stopped: ...` | The capture failed. Send me the message and the host log (`fogplay host --debug`) |
| Black screen in a game | The game uses exclusive fullscreen, which screen capture can't see. Switch it to borderless or windowed |
| Session fails after 60 s with `timed out before streaming started` | The page never connected; see the rows above |
| Keys like Esc or Alt+Tab act on the player PC | Browsers only let a page capture those over https. Expected for now |

On a Linux host, capture works under X11 and the stream is view-only: input
replay is Windows-only so far.

Downloaded FFmpeg lives in the user cache folder (`%LOCALAPPDATA%\fogplay\ffmpeg`
on Windows). Delete it to force a fresh download.

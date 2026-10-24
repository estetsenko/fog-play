# Third-party programs

fogplay downloads these programs from their official GitHub releases the first
time they're needed, and runs them as separate processes. They aren't
compiled into fogplay, and fogplay talks to them only through their command
line and HTTP interfaces.

| Program | Used by | License | Source |
| --- | --- | --- | --- |
| Sunshine | `fogplay host` streams the PC with it | GPL-3.0 | https://github.com/LizardByte/Sunshine |
| Moonlight (moonlight-qt) | `fogplay play` shows the stream with it | GPL-3.0 | https://github.com/moonlight-stream/moonlight-qt |

Each download is the unmodified upstream release. The full source of each version is
available from the links above, under the release tag fogplay downloaded
(recorded in `installed.json` in the cache folder).

Our own WebRTC streaming engine, which replaces both, is on the roadmap.

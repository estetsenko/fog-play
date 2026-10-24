# Third-party programs

`fogplay host` captures and encodes the screen with FFmpeg, run as a separate
process. It isn't compiled into fogplay; fogplay only passes it a command line
and reads the encoded video from its output.

| Program | Used by | License | Source |
| --- | --- | --- | --- |
| FFmpeg (BtbN Windows build, GPL variant) | `fogplay host` on Windows, downloaded on first use | GPL-3.0 | https://github.com/BtbN/FFmpeg-Builds, https://ffmpeg.org |

On Linux, fogplay uses the FFmpeg from your package manager. If `ffmpeg` is on
PATH it is used on any OS.

The download is the unmodified upstream build (`ffmpeg-master-latest-win64-gpl.zip`).
Its full source is available from the links above; the release fogplay
downloaded is recorded in `installed.json` in the cache folder.

Go libraries compiled into fogplay are listed in `go.mod`; the WebRTC stack is
[Pion](https://github.com/pion/webrtc) (MIT).

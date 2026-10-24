package stream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
)

// Encoder is an FFmpeg H.264 encoder and the options that make it low latency.
type Encoder struct {
	Name string
	// Args returns the codec options for a bitrate in bits per second.
	Args func(bitrate int) []string
}

// Encoders are tried in this order; the first that works on this PC is used.
// Every one is set up for one slice per frame and no B-frames, which the
// packetizer in run relies on.
var Encoders = []Encoder{
	{"h264_nvenc", func(b int) []string {
		return []string{"-preset", "p1", "-tune", "ull", "-zerolatency", "1", "-rc", "cbr", "-b:v", br(b), "-profile:v", "baseline"}
	}},
	{"h264_amf", func(b int) []string {
		return []string{"-usage", "ultralowlatency", "-quality", "speed", "-rc", "cbr", "-b:v", br(b), "-profile:v", "constrained_baseline"}
	}},
	{"h264_qsv", func(b int) []string {
		return []string{"-preset", "veryfast", "-async_depth", "1", "-b:v", br(b), "-profile:v", "baseline"}
	}},
	{"libx264", func(b int) []string {
		return []string{"-preset", "ultrafast", "-tune", "zerolatency", "-x264-params", "sliced-threads=0", "-b:v", br(b), "-profile:v", "baseline"}
	}},
}

func br(bitrate int) string { return strconv.Itoa(bitrate) }

// PickEncoder returns the first of Encoders that can encode a few frames here.
func PickEncoder(ctx context.Context, ffmpeg string) (Encoder, error) {
	var tried []string
	for _, e := range Encoders {
		args := []string{"-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "color=black:size=1280x720:rate=60",
			"-frames:v", "3", "-pix_fmt", "nv12", "-c:v", e.Name}
		args = append(args, e.Args(10_000_000)...)
		args = append(args, "-f", "null", "-")
		pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := exec.CommandContext(pctx, ffmpeg, args...).Run()
		cancel()
		if err == nil {
			return e, nil
		}
		if ctx.Err() != nil {
			return Encoder{}, ctx.Err()
		}
		tried = append(tried, e.Name)
	}
	return Encoder{}, fmt.Errorf("no H.264 encoder works with this FFmpeg (tried %s)", strings.Join(tried, ", "))
}

// Capture says what FFmpeg records. With Input empty, Filter is a source
// filter graph (-filter_complex); otherwise Filter is applied to Input (-vf).
type Capture struct {
	Input  []string
	Filter string
}

// ScreenCapture records the primary display at fps frames per second.
func ScreenCapture(fps int) (Capture, error) {
	f := strconv.Itoa(fps)
	switch runtime.GOOS {
	case "windows":
		// Desktop Duplication, copied to system memory for any encoder.
		return Capture{Filter: "ddagrab=output_idx=0:draw_mouse=1:framerate=" + f + ",hwdownload,format=bgra,format=nv12"}, nil
	case "linux":
		return Capture{Input: []string{"-f", "x11grab", "-framerate", f, "-draw_mouse", "1", "-i", displayName()}, Filter: "format=nv12"}, nil
	}
	return Capture{}, fmt.Errorf("screen capture is not supported on %s yet", runtime.GOOS)
}

// TestCapture is a moving test pattern, for trying the stream without a screen.
func TestCapture(width, height, fps int) Capture {
	return Capture{
		Input:  []string{"-re", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=%d", width, height, fps)},
		Filter: "format=nv12",
	}
}

// encodeArgs builds the FFmpeg command line that writes raw H.264 to stdout.
func encodeArgs(c Capture, e Encoder, bitrate, gop int) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-fflags", "nobuffer"}
	if len(c.Input) == 0 {
		args = append(args, "-filter_complex", c.Filter)
	} else {
		args = append(args, c.Input...)
		args = append(args, "-vf", c.Filter)
	}
	args = append(args, "-an", "-c:v", e.Name)
	args = append(args, e.Args(bitrate)...)
	// A keyframe every gop frames, each carrying SPS/PPS, so a player that
	// joins or loses packets recovers within that time.
	args = append(args, "-g", strconv.Itoa(gop), "-bf", "0", "-bsf:v", "dump_extra=freq=keyframe",
		"-flush_packets", "1", "-f", "h264", "-")
	return args
}

// encode runs FFmpeg until ctx ends and writes each frame to write. It
// returns an error if FFmpeg stops on its own.
func encode(ctx context.Context, ffmpeg string, args []string, write func(media.Sample) error, log *slog.Logger) error {
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	errOut, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	log.Debug("starting ffmpeg", "args", strings.Join(args, " "))
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ffmpeg: %w", err)
	}
	lastErr := make(chan string, 1)
	go func() {
		var last string
		sc := bufio.NewScanner(errOut)
		for sc.Scan() {
			last = sc.Text()
			log.Warn("ffmpeg", "msg", last)
		}
		lastErr <- last
	}()

	readErr := packetize(out, write)
	io.Copy(io.Discard, out)
	waitErr := cmd.Wait()
	msg := <-lastErr
	if ctx.Err() != nil {
		return nil
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if msg != "" {
		return fmt.Errorf("ffmpeg stopped: %s", msg)
	}
	return fmt.Errorf("ffmpeg stopped: %v", waitErr)
}

// packetize reads an H.264 byte stream and hands out one sample per frame:
// the frame's slice plus any parameter sets before it, in Annex B form.
func packetize(r io.Reader, write func(media.Sample) error) error {
	rd, err := h264reader.NewReader(r)
	if err != nil {
		return err
	}
	var au []byte
	last := time.Now()
	for {
		nal, err := rd.NextNAL()
		if err != nil {
			return err
		}
		au = append(au, 0, 0, 0, 1)
		au = append(au, nal.Data...)
		if nal.UnitType != h264reader.NalUnitTypeCodedSliceNonIdr && nal.UnitType != h264reader.NalUnitTypeCodedSliceIdr {
			continue
		}
		now := time.Now()
		d := now.Sub(last)
		if d <= 0 {
			d = time.Millisecond
		}
		last = now
		if err := write(media.Sample{Data: au, Duration: d}); err != nil {
			return err
		}
		au = nil
	}
}

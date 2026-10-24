package stream

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	"github.com/pion/webrtc/v4/pkg/media"
)

func needFFmpeg(t *testing.T) string {
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	return p
}

// libx264 is the fallback encoder; tests use it because CI has no GPU.
func x264(t *testing.T) Encoder {
	for _, e := range Encoders {
		if e.Name == "libx264" {
			return e
		}
	}
	t.Fatal("no libx264 encoder")
	return Encoder{}
}

// Each frame becomes one sample, and every keyframe carries SPS and PPS so a
// player can start decoding there.
func TestPacketizeOneSamplePerFrame(t *testing.T) {
	ffmpeg := needFFmpeg(t)
	c := Capture{Input: []string{"-f", "lavfi", "-i", "testsrc2=size=320x240:rate=30", "-frames:v", "45"}, Filter: "format=nv12"}
	out, err := exec.Command(ffmpeg, encodeArgs(c, x264(t), 1_000_000, 15)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	var samples []media.Sample
	err = packetize(bytes.NewReader(out), func(s media.Sample) error { samples = append(samples, s); return nil })
	if err == nil {
		t.Fatal("packetize should end with EOF")
	}
	if len(samples) != 45 {
		t.Fatalf("got %d samples, want 45", len(samples))
	}
	for i, s := range samples {
		hasSPS := bytes.Contains(s.Data, []byte{0, 0, 0, 1, 0x67})
		if (i%15 == 0) != hasSPS {
			t.Errorf("sample %d: SPS present = %v", i, hasSPS)
		}
	}
}

func TestPickEncoderFallsBackToX264(t *testing.T) {
	ffmpeg := needFFmpeg(t)
	e, err := PickEncoder(context.Background(), ffmpeg)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("picked %s", e.Name)
}

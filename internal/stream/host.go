// Package stream is Fog Play's own streaming engine, host side: it captures
// the screen with FFmpeg, encodes it on the GPU, sends it to the player's
// browser over WebRTC, and replays the player's mouse and keyboard.
package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/pion/interceptor"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"

	"github.com/estetsenko/fog-play/internal/signal"
)

// Signal is the host's side of a session's signaling relay.
type Signal interface {
	Send(ctx context.Context, typ string, data any) error
	Receive(ctx context.Context) (signal.Message, error)
}

type Host struct {
	FFmpeg  string
	Encoder Encoder
	Capture Capture
	Bitrate int // bits per second
	GOP     int // frames between keyframes
	Input   Injector
	Log     *slog.Logger
}

// h264 is the only codec offered: constrained baseline, which every browser
// decodes in hardware.
var h264 = webrtc.RTPCodecCapability{
	MimeType:     webrtc.MimeTypeH264,
	ClockRate:    90000,
	SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
	RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}},
}

func newAPI() (*webrtc.API, error) {
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: h264, PayloadType: 102}, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	ir := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, ir); err != nil {
		return nil, err
	}
	return webrtc.NewAPI(webrtc.WithMediaEngine(m), webrtc.WithInterceptorRegistry(ir)), nil
}

// Serve streams to the session's player until ctx ends or the encoder fails.
// The encoder starts when the first player connects and keeps running if
// the player reconnects, for example after reloading the page.
func (h *Host) Serve(ctx context.Context, sig Signal) error {
	if h.Bitrate == 0 {
		h.Bitrate = 15_000_000
	}
	if h.GOP == 0 {
		h.GOP = 60
	}
	api, err := newAPI()
	if err != nil {
		return err
	}
	track, err := webrtc.NewTrackLocalStaticSample(h264, "video", "fogplay")
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	encErr := make(chan error, 1)
	var startOnce sync.Once
	start := func() {
		startOnce.Do(func() {
			h.Log.Info("player connected, starting stream", "encoder", h.Encoder.Name)
			args := encodeArgs(h.Capture, h.Encoder, h.Bitrate, h.GOP)
			go func() {
				encErr <- encode(ctx, h.FFmpeg, args, func(s media.Sample) error {
					err := track.WriteSample(s)
					if errors.Is(err, io.ErrClosedPipe) {
						return nil
					}
					return err
				}, h.Log)
			}()
		})
	}

	msgs := make(chan signal.Message)
	recvErr := make(chan error, 1)
	go func() {
		for {
			m, err := sig.Receive(ctx)
			if err != nil {
				recvErr <- err
				return
			}
			select {
			case msgs <- m:
			case <-ctx.Done():
				return
			}
		}
	}()

	var pc *webrtc.PeerConnection
	defer func() {
		if pc != nil {
			pc.Close()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-recvErr:
			if ctx.Err() != nil {
				return nil
			}
			h.Log.Info("signaling closed", "err", err)
			return nil
		case err := <-encErr:
			if err != nil {
				return err
			}
			return nil
		case m := <-msgs:
			switch m.Type {
			case signal.TypeHello:
				if pc != nil {
					pc.Close()
				}
				pc, err = h.offer(ctx, api, track, sig, start)
				if err != nil {
					return fmt.Errorf("create offer: %w", err)
				}
			case signal.TypeAnswer:
				var a signal.SDP
				if json.Unmarshal(m.Data, &a) != nil || pc == nil {
					continue
				}
				if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: a.SDP}); err != nil {
					h.Log.Warn("bad answer from player", "err", err)
				}
			}
		}
	}
}

// offer makes a peer connection with the video track and the input channel
// and sends its offer once all ICE candidates are gathered.
func (h *Host) offer(ctx context.Context, api *webrtc.API, track webrtc.TrackLocal, sig Signal, connected func()) (*webrtc.PeerConnection, error) {
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return nil, err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		pc.Close()
		return nil, err
	}
	// RTCP has to be read for NACKs to be answered.
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}()
	dc, err := pc.CreateDataChannel("input", nil)
	if err != nil {
		pc.Close()
		return nil, err
	}
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		var e InputEvent
		if json.Unmarshal(msg.Data, &e) != nil {
			return
		}
		if err := h.Input.Inject(e); err != nil {
			h.Log.Debug("inject input", "err", err)
		}
	})
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		h.Log.Debug("peer connection", "state", s.String())
		if s == webrtc.PeerConnectionStateConnected {
			connected()
		}
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		pc.Close()
		return nil, err
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		pc.Close()
		return nil, err
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		pc.Close()
		return nil, ctx.Err()
	}
	if err := sig.Send(ctx, signal.TypeOffer, signal.SDP{SDP: pc.LocalDescription().SDP}); err != nil {
		pc.Close()
		return nil, err
	}
	return pc, nil
}

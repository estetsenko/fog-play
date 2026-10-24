// Package player runs the player side of a session: book a host, pair
// Moonlight with the host's Sunshine, stream, and end the session.
package player

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/estetsenko/fog-play/internal/client"
	"github.com/estetsenko/fog-play/internal/signal"
)

// Moonlight runs the Moonlight client. Each call blocks until it exits.
type Moonlight interface {
	Pair(ctx context.Context, address, pin string) error
	Stream(ctx context.Context, address, app string) error
}

type Options struct {
	Game      string
	LatencyMS map[string]int
	// ClientName is how this PC shows up in Sunshine's pairing list.
	ClientName string
	// HostInfoTimeout bounds the wait for the host agent to answer.
	HostInfoTimeout time.Duration
	// Started, if set, is called once streaming begins.
	Started func(address, app string)
}

// Play runs a whole session. It always ends the session it created, also
// when ctx is cancelled (for example on Ctrl+C).
func Play(ctx context.Context, api *client.Client, ml Moonlight, opt Options, log *slog.Logger) (err error) {
	if opt.HostInfoTimeout == 0 {
		opt.HostInfoTimeout = 60 * time.Second
	}
	sess, err := api.CreateSession(ctx, opt.Game, opt.LatencyMS)
	if err != nil {
		return fmt.Errorf("book a host: %w", err)
	}
	log = log.With("session_id", sess.ID)
	log.Info("host booked", "region", sess.HostRegion)
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err != nil {
			api.SetSessionState(cctx, sess.ID, "failed", err.Error())
		} else {
			api.EndSession(cctx, sess.ID)
		}
	}()

	sig, err := api.Signal(ctx, sess.ID)
	if err != nil {
		return fmt.Errorf("connect to signaling: %w", err)
	}
	defer sig.Close()

	waitCtx, cancel := context.WithTimeout(ctx, opt.HostInfoTimeout)
	info, err := receive[signal.HostInfo](waitCtx, sig, signal.TypeHostInfo)
	cancel()
	if err != nil {
		return fmt.Errorf("waiting for the host to answer: %w", err)
	}
	log.Info("host ready", "address", info.Address, "app", info.App)

	pin, err := randomPIN()
	if err != nil {
		return err
	}
	if err := sig.Send(ctx, signal.TypePairPIN, signal.PairPIN{PIN: pin, ClientName: opt.ClientName}); err != nil {
		return err
	}
	// Moonlight's pair blocks until the host enters the PIN, which the
	// agent does once it sees Moonlight's request in Sunshine.
	pairCtx, cancelPair := context.WithCancel(ctx)
	defer cancelPair()
	pairErr := make(chan error, 1)
	go func() { pairErr <- ml.Pair(pairCtx, info.Address, pin) }()
	type pairReply struct {
		res signal.PairResult
		err error
	}
	replyCh := make(chan pairReply, 1)
	go func() {
		res, err := receive[signal.PairResult](pairCtx, sig, signal.TypePairResult)
		replyCh <- pairReply{res, err}
	}()
	// Both must succeed: Moonlight finishing its side and the host confirming.
	var moonlightDone, hostDone bool
	for !moonlightDone || !hostDone {
		select {
		case err := <-pairErr:
			if err != nil {
				return fmt.Errorf("moonlight pair: %w", err)
			}
			moonlightDone = true
		case r := <-replyCh:
			if r.err != nil {
				return fmt.Errorf("waiting for pairing: %w", r.err)
			}
			if !r.res.OK {
				return fmt.Errorf("host could not pair: %s", r.res.Error)
			}
			hostDone = true
		}
	}
	log.Info("paired")

	if err := api.SetSessionState(ctx, sess.ID, "active", ""); err != nil {
		return fmt.Errorf("start session: %w", err)
	}
	if opt.Started != nil {
		opt.Started(info.Address, info.App)
	}
	if err := ml.Stream(ctx, info.Address, info.App); err != nil && ctx.Err() == nil {
		return fmt.Errorf("moonlight stream: %w", err)
	}
	return nil
}

// receive waits for the next message of the given type and decodes its data.
func receive[T any](ctx context.Context, sig *client.Signal, typ string) (T, error) {
	var out T
	for {
		m, err := sig.Receive(ctx)
		if errors.Is(err, client.ErrSignalClosed) {
			return out, errors.New("session closed")
		}
		if err != nil {
			return out, err
		}
		if m.Type == typ {
			return out, json.Unmarshal(m.Data, &out)
		}
	}
}

func randomPIN() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(10000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%04d", n.Int64()), nil
}

// Package player runs the player side of a session: book a host, open the
// stream in the browser, and end the session when the player is done.
package player

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"runtime"
	"time"

	"github.com/estetsenko/fog-play/internal/client"
)

type Options struct {
	Game      string
	LatencyMS map[string]int
	// Open shows the play page; nil uses the system browser.
	Open func(url string) error
	// PollInterval is how often the session state is checked.
	PollInterval time.Duration
	// Opened, if set, is called with the play page URL once it is open.
	Opened func(url string)
}

// Play runs a whole session. The browser page does the streaming; Play
// waits until the session ends there or on the host. It always ends the
// session it created, also when ctx is cancelled (for example on Ctrl+C).
func Play(ctx context.Context, api *client.Client, opt Options, log *slog.Logger) (err error) {
	if opt.Open == nil {
		opt.Open = OpenBrowser
	}
	if opt.PollInterval == 0 {
		opt.PollInterval = 2 * time.Second
	}
	sess, err := api.CreateSession(ctx, opt.Game, opt.LatencyMS)
	if err != nil {
		return fmt.Errorf("book a host: %w", err)
	}
	log = log.With("session_id", sess.ID)
	log.Info("host booked", "region", sess.HostRegion)
	ended := false
	defer func() {
		if ended {
			return
		}
		cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err != nil {
			api.SetSessionState(cctx, sess.ID, "failed", err.Error())
		} else {
			api.EndSession(cctx, sess.ID)
		}
	}()

	page := PlayURL(api, sess.ID)
	if err := opt.Open(page); err != nil {
		return fmt.Errorf("open browser: %w; open this page yourself: %s", err, page)
	}
	if opt.Opened != nil {
		opt.Opened(page)
	}

	t := time.NewTicker(opt.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		s, err := api.Session(ctx, sess.ID)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Warn("check session failed", "err", err)
			continue
		}
		switch s.State {
		case "ended":
			ended = true
			return nil
		case "failed":
			ended = true
			return errors.New(s.FailureReason)
		}
	}
}

// PlayURL is the browser page that streams the session. The token rides in
// the fragment, which browsers never send to the server or in Referer.
func PlayURL(api *client.Client, sessionID string) string {
	v := url.Values{"s": {sessionID}, "t": {api.Token}}
	return api.BaseURL + "/play#" + v.Encode()
}

// OpenBrowser opens url in the system's default browser.
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

//go:build !windows

package stream

import "log/slog"

type logInput struct{ log *slog.Logger }

// NewInjector returns an injector that only logs: replaying input is
// implemented for Windows hosts so far.
func NewInjector(log *slog.Logger) Injector {
	log.Warn("player input is not supported on this OS yet; the stream is view-only")
	return logInput{log}
}

func (l logInput) Inject(e InputEvent) error {
	l.log.Debug("input", "event", e)
	return nil
}

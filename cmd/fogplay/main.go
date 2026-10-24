// Command fogplay runs the Fog Play control plane API.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/estetsenko/fog-play/internal/api"
	"github.com/estetsenko/fog-play/internal/auth"
	"github.com/estetsenko/fog-play/internal/config"
	"github.com/estetsenko/fog-play/internal/db"
	"github.com/estetsenko/fog-play/internal/presence"
	"github.com/estetsenko/fog-play/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		return err
	}

	pres, err := presence.NewRedis(cfg.RedisURL, cfg.HeartbeatTTL)
	if err != nil {
		return err
	}
	defer pres.Close()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(store.New(pool), pres, auth.NewIssuer(cfg.JWTSecret, cfg.TokenTTL), log).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

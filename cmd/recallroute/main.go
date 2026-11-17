// Command recallroute runs the RecallRoute HTTP server.
//
// Configuration is read from the environment (see .env.example):
//
//	RECALLROUTE_ADDR           listen address (default :8080)
//	DATABASE_URL               PostgreSQL URL; in-memory store if empty
//	RECALLROUTE_API_TOKEN      bearer token required for writes; open if empty
//	RECALLROUTE_FEED_URL       recall feed to poll; polling off if empty
//	RECALLROUTE_POLL_INTERVAL  e.g. 6h (default 6h)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yashchhabra57/recallroute/internal/api"
	"github.com/yashchhabra57/recallroute/internal/hub"
	"github.com/yashchhabra57/recallroute/internal/poller"
	"github.com/yashchhabra57/recallroute/internal/service"
	"github.com/yashchhabra57/recallroute/internal/store"
	"github.com/yashchhabra57/recallroute/internal/store/memstore"
	"github.com/yashchhabra57/recallroute/internal/store/pgstore"
	"github.com/yashchhabra57/recallroute/web"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var st store.Store
	kind := "memory"
	if url := os.Getenv("DATABASE_URL"); url != "" {
		pg, err := pgstore.Open(ctx, url)
		if err != nil {
			return err
		}
		st, kind = pg, "postgres"
	} else {
		st = memstore.New()
		log.Warn("DATABASE_URL not set: using in-memory store; data is lost on restart")
	}
	defer st.Close()

	h := hub.New(log)
	svc := service.New(st, h, service.WithLogger(log))

	cfg := api.Config{
		Token:     os.Getenv("RECALLROUTE_API_TOKEN"),
		StoreKind: kind,
		Static:    web.Static(),
		WS:        h,
		Log:       log,
	}
	if cfg.Token == "" {
		log.Warn("RECALLROUTE_API_TOKEN not set: write endpoints are open")
	}

	if feedURL := os.Getenv("RECALLROUTE_FEED_URL"); feedURL != "" {
		interval, err := time.ParseDuration(env("RECALLROUTE_POLL_INTERVAL", "6h"))
		if err != nil || interval < time.Minute {
			return fmt.Errorf("RECALLROUTE_POLL_INTERVAL must be a duration of at least 1m")
		}
		p := &poller.Poller{URL: feedURL, Interval: interval, Ingest: svc.IngestRecalls, Log: log}
		cfg.Poll = func(r *http.Request) (service.IngestSummary, error) {
			sum, _, err := p.PollOnce(r.Context())
			return sum, err
		}
		go p.Run(ctx)
		log.Info("feed polling on", "url", feedURL, "interval", interval)
	}

	srv := &http.Server{
		Addr:              env("RECALLROUTE_ADDR", ":8080"),
		Handler:           api.New(svc, cfg),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", srv.Addr, "store", kind)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
	}
	return nil
}

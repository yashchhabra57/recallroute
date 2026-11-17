package poller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/service"
)

func feedServer(t *testing.T, status *atomic.Int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	body, err := os.ReadFile("../../testdata/openfda_sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if s := status.Load(); s != 0 {
			w.WriteHeader(int(s))
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestPollOnceUsesETag(t *testing.T) {
	var status atomic.Int32
	srv, hits := feedServer(t, &status)
	var ingested int
	p := &Poller{URL: srv.URL, Ingest: func(_ context.Context, rs []domain.Recall) (service.IngestSummary, error) {
		ingested += len(rs)
		return service.IngestSummary{Received: len(rs)}, nil
	}}

	sum, changed, err := p.PollOnce(context.Background())
	if err != nil || !changed || sum.Received != 5 {
		t.Fatalf("first poll: %+v %v %v", sum, changed, err)
	}
	_, changed, err = p.PollOnce(context.Background())
	if err != nil || changed {
		t.Fatalf("second poll should be 304: changed=%v err=%v", changed, err)
	}
	if ingested != 5 || hits.Load() != 2 {
		t.Errorf("ingested=%d hits=%d", ingested, hits.Load())
	}
}

func TestOpenFDA404MeansNoResults(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusNotFound)
	srv, _ := feedServer(t, &status)
	p := &Poller{URL: srv.URL, Ingest: func(context.Context, []domain.Recall) (service.IngestSummary, error) {
		t.Fatal("ingest should not run")
		return service.IngestSummary{}, nil
	}}
	if _, changed, err := p.PollOnce(context.Background()); err != nil || changed {
		t.Errorf("changed=%v err=%v", changed, err)
	}
}

func TestServerErrorAndIngestFailureKeepNoETag(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	srv, _ := feedServer(t, &status)
	fail := true
	p := &Poller{URL: srv.URL, Ingest: func(context.Context, []domain.Recall) (service.IngestSummary, error) {
		if fail {
			return service.IngestSummary{}, errors.New("db down")
		}
		return service.IngestSummary{Received: 5}, nil
	}}
	if _, _, err := p.PollOnce(context.Background()); !errors.Is(err, ErrFeedStatus) {
		t.Fatalf("503: err = %v", err)
	}
	status.Store(0)
	if _, _, err := p.PollOnce(context.Background()); err == nil {
		t.Fatal("ingest failure should surface")
	}
	fail = false
	// Because the failed ingest did not store the ETag, this is a full
	// fetch, not a 304.
	if sum, changed, err := p.PollOnce(context.Background()); err != nil || !changed || sum.Received != 5 {
		t.Errorf("retry: %+v %v %v", sum, changed, err)
	}
}

func TestBackoff(t *testing.T) {
	hour := time.Hour
	cases := map[int]time.Duration{0: hour, 1: 5 * time.Second, 2: 10 * time.Second, 4: 40 * time.Second, 20: hour}
	for n, want := range cases {
		if got := Backoff(hour, n, 0); got != want {
			t.Errorf("Backoff(%d) = %v, want %v", n, got, want)
		}
	}
	if got := Backoff(hour, 1, 0.999); got <= 5*time.Second || got > 6*time.Second {
		t.Errorf("jitter out of range: %v", got)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	var status atomic.Int32
	srv, hits := feedServer(t, &status)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	p := &Poller{URL: srv.URL, Interval: time.Hour, Ingest: func(context.Context, []domain.Recall) (service.IngestSummary, error) {
		cancel()
		return service.IngestSummary{}, nil
	}}
	go func() { p.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d", hits.Load())
	}
}

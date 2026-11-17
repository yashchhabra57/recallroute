// Package poller fetches a recall feed on an interval and hands new
// recalls to the service.
//
// It uses conditional requests (ETag / Last-Modified) so an unchanged
// feed costs one 304, and it backs off exponentially when the feed is
// down so it does not hammer a public API that is already struggling.
package poller

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/yashchhabra57/recallroute/internal/domain"
	"github.com/yashchhabra57/recallroute/internal/feed"
	"github.com/yashchhabra57/recallroute/internal/service"
)

// IngestFunc stores recalls (service.IngestRecalls in production).
type IngestFunc func(context.Context, []domain.Recall) (service.IngestSummary, error)

// Poller polls one feed URL.
type Poller struct {
	URL      string
	Interval time.Duration
	Client   *http.Client
	Ingest   IngestFunc
	Log      *slog.Logger

	mu           sync.Mutex // one poll at a time (timer and manual trigger)
	etag         string
	lastModified string
}

// ErrFeedStatus is returned for unexpected HTTP status codes.
var ErrFeedStatus = errors.New("poller: unexpected feed status")

// PollOnce fetches the feed. It returns changed=false when the server
// answered 304 Not Modified.
func (p *Poller) PollOnce(ctx context.Context) (sum service.IngestSummary, changed bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
	if err != nil {
		return sum, false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "recallroute-poller/1.0")
	if p.etag != "" {
		req.Header.Set("If-None-Match", p.etag)
	}
	if p.lastModified != "" {
		req.Header.Set("If-Modified-Since", p.lastModified)
	}

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return sum, false, fmt.Errorf("poller: fetch: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return sum, false, nil
	case http.StatusNotFound:
		// openFDA answers a search with zero results with 404 and a
		// NOT_FOUND error body. For a date-window query that just means
		// "no recalls this week".
		_, _ = io.Copy(io.Discard, resp.Body)
		return sum, false, nil
	case http.StatusOK:
	default:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return sum, false, fmt.Errorf("%w: %s", ErrFeedStatus, resp.Status)
	}

	rs, err := feed.ParseOpenFDA(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return sum, false, err
	}
	sum, err = p.Ingest(ctx, rs)
	if err != nil {
		return sum, false, err
	}
	// Only remember validators after a successful ingest, so a failed
	// ingest is retried with a full fetch.
	p.etag = resp.Header.Get("ETag")
	p.lastModified = resp.Header.Get("Last-Modified")
	return sum, true, nil
}

// Run polls until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	log := p.Log
	if log == nil {
		log = slog.Default()
	}
	failures := 0
	delay := time.Duration(0) // first poll immediately
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		sum, changed, err := p.PollOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			delay = Backoff(p.Interval, failures, rand.Float64())
			log.Warn("feed poll failed", "url", p.URL, "err", err, "failures", failures, "retry_in", delay)
			continue
		}
		failures = 0
		delay = p.Interval
		log.Info("feed polled", "changed", changed, "recalls", sum.Received, "new_alerts", len(sum.NewAlerts))
	}
}

// Backoff returns the wait after the nth consecutive failure: 5s, 10s,
// 20s... capped at interval, with up to 20% jitter so many instances do
// not retry in lockstep. jitter is a number in [0,1).
func Backoff(interval time.Duration, failures int, jitter float64) time.Duration {
	if failures < 1 {
		return interval
	}
	d := 5 * time.Second
	for i := 1; i < failures && d < interval; i++ {
		d *= 2
	}
	if d > interval {
		d = interval
	}
	return d + time.Duration(float64(d)*0.2*jitter)
}

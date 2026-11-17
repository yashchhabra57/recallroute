// Command smoke drives a running RecallRoute server end to end: it opens
// a WebSocket, imports the demo inventory, ingests the demo feed, checks
// that live events arrive, and walks one alert through the workflow.
//
//	go run ./scripts/smoke -base http://localhost:8080 [-token T]
//
// It exits non-zero on the first failed check. Run it against a fresh
// server (it expects the demo data to create five alerts).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"
)

var (
	base  = flag.String("base", "http://localhost:8080", "server base URL")
	token = flag.String("token", "", "API token, if the server requires one")
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "SMOKE FAIL:", err)
		os.Exit(1)
	}
	fmt.Println("SMOKE OK")
}

func call(method, path, ctype string, body []byte, out any) (int, error) {
	req, err := http.NewRequest(method, *base+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if *token != "" {
		req.Header.Set("Authorization", "Bearer "+*token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: %w (body %q)", method, path, err, b)
		}
	}
	return resp.StatusCode, nil
}

func fetch(path string) ([]byte, error) {
	resp, err := http.Get(*base + path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func check(ok bool, format string, args ...any) error {
	if !ok {
		return fmt.Errorf(format, args...)
	}
	fmt.Println("  ok:", fmt.Sprintf(format, args...))
	return nil
}

type alert struct {
	ID      int64  `json:"id"`
	Tier    string `json:"tier"`
	State   string `json:"state"`
	Version int    `json:"version"`
	Lot     struct {
		SKU      string `json:"sku"`
		Quantity int    `json:"quantity"`
	} `json:"lot"`
	Events []json.RawMessage `json:"events"`
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var health map[string]any
	if code, err := call("GET", "/healthz", "", nil, &health); err != nil || code != 200 {
		return fmt.Errorf("healthz: %d %v", code, err)
	}
	fmt.Println("server:", health)

	wsURL := "ws" + strings.TrimPrefix(*base, "http") + "/ws"
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("websocket dial: %w", err)
	}
	defer ws.CloseNow() //nolint:errcheck
	events := make(chan string, 256)
	go func() {
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				close(events)
				return
			}
			var m struct{ Type string }
			_ = json.Unmarshal(b, &m)
			events <- m.Type
		}
	}()
	if t := <-events; t != "hello" {
		return fmt.Errorf("first ws message %q, want hello", t)
	}

	csv, err := fetch("/demo/inventory_sample.csv")
	if err != nil {
		return err
	}
	var imp struct {
		Imported int `json:"imported"`
	}
	if code, err := call("POST", "/api/lots", "text/csv", csv, &imp); err != nil || code != 200 {
		return fmt.Errorf("import: %d %v", code, err)
	}
	if err := check(imp.Imported == 9, "imported %d lots", imp.Imported); err != nil {
		return err
	}

	feed, err := fetch("/demo/openfda_sample.json")
	if err != nil {
		return err
	}
	var ing struct {
		Received  int   `json:"received"`
		NewAlerts []any `json:"new_alerts"`
	}
	if code, err := call("POST", "/api/recalls/ingest", "application/json", feed, &ing); err != nil || code != 200 {
		return fmt.Errorf("ingest: %d %v", code, err)
	}
	if err := check(ing.Received == 5 && len(ing.NewAlerts) == 5, "ingested %d recalls -> %d alerts", ing.Received, len(ing.NewAlerts)); err != nil {
		return err
	}

	created := 0
	deadline := time.After(5 * time.Second)
	for created < 5 {
		select {
		case t, ok := <-events:
			if !ok {
				return fmt.Errorf("websocket closed early")
			}
			if t == "alert.created" {
				created++
			}
		case <-deadline:
			return fmt.Errorf("only %d alert.created events over websocket", created)
		}
	}
	_ = check(true, "received %d alert.created events over websocket", created)

	var alerts []alert
	if _, err := call("GET", "/api/alerts?state=new", "", nil, &alerts); err != nil {
		return err
	}
	var target alert
	for _, a := range alerts {
		if a.Lot.SKU == "WFS-8" {
			target = a
		}
	}
	if err := check(target.ID != 0 && target.Tier == "confirmed", "whitefish lot flagged as %s", target.Tier); err != nil {
		return err
	}

	path := fmt.Sprintf("/api/alerts/%d/transitions", target.ID)
	steps := []map[string]any{
		{"action": "acknowledge", "actor": "smoke"},
		{"action": "pull", "actor": "smoke", "quantity": 12},
		{"action": "notify", "actor": "smoke", "recipients": []string{"Hope Pantry", "Eastside Shelter"}},
		{"action": "close", "actor": "smoke"},
	}
	v := target
	for _, s := range steps {
		s["version"] = v.Version
		body, _ := json.Marshal(s)
		code, err := call("POST", path, "application/json", body, &v)
		if err != nil || code != 200 {
			return fmt.Errorf("%s: %d %v", s["action"], code, err)
		}
		fmt.Printf("  ok: %s -> %s (v%d)\n", s["action"], v.State, v.Version)
	}
	if err := check(v.State == "closed" && v.Lot.Quantity == 0 && len(v.Events) == 4, "closed with 0 left on shelf and %d audit events", len(v.Events)); err != nil {
		return err
	}

	var e struct{ Code string }
	body, _ := json.Marshal(map[string]any{"version": 1, "action": "acknowledge", "actor": "smoke"})
	code, _ := call("POST", path, "application/json", body, &e)
	return check(code == 409 && e.Code == "version_conflict", "stale write rejected with %d %s", code, e.Code)
}

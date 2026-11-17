// Package hub fans server events out to WebSocket clients so every
// volunteer's board updates the moment an alert is created or moved.
package hub

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Message is the envelope every client receives.
type Message struct {
	Type string    `json:"type"`
	At   time.Time `json:"at"`
	Data any       `json:"data"`
}

// BufferSize is how many messages a client may fall behind before it is
// disconnected. Disconnected clients reconnect and refetch, which is
// cheaper than letting one slow phone hold memory for everyone.
const BufferSize = 64

// Hub is a broadcast hub. The zero value is not usable; call New.
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
	log  *slog.Logger
}

// New returns an empty hub.
func New(log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	return &Hub{subs: map[chan []byte]struct{}{}, log: log}
}

// Publish sends a message to every subscriber without blocking.
func (h *Hub) Publish(typ string, data any) {
	b, err := json.Marshal(Message{Type: typ, At: time.Now().UTC(), Data: data})
	if err != nil {
		h.log.Error("hub: marshal", "type", typ, "err", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default:
			// Too slow: drop it. Its writer sees the closed channel and
			// closes the socket.
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Subscribe registers a new subscriber. Call cancel when done.
func (h *Hub) Subscribe() (msgs <-chan []byte, cancel func()) {
	ch := make(chan []byte, BufferSize)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Count returns the number of live subscribers.
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// ServeHTTP upgrades the request to a WebSocket and streams messages
// until either side goes away.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept already wrote the error response
	}
	defer c.CloseNow() //nolint:errcheck

	msgs, cancel := h.Subscribe()
	defer cancel()

	// We never expect messages from the client; CloseRead handles control
	// frames and cancels ctx when the client disconnects.
	ctx := c.CloseRead(r.Context())

	hello, _ := json.Marshal(Message{Type: "hello", At: time.Now().UTC(), Data: map[string]int{"clients": h.Count()}})
	if err := write(ctx, c, hello); err != nil {
		return
	}

	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case b, ok := <-msgs:
			if !ok {
				c.Close(websocket.StatusPolicyViolation, "client too slow; reconnect") //nolint:errcheck
				return
			}
			if err := write(ctx, c, b); err != nil {
				return
			}
		case <-ping.C:
			pctx, done := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			done()
			if err != nil {
				return
			}
		}
	}
}

func write(ctx context.Context, c *websocket.Conn, b []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(ctx, websocket.MessageText, b)
}

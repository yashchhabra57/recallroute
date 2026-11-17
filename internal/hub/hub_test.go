package hub

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestPublishReachesSubscribers(t *testing.T) {
	h := New(nil)
	a, cancelA := h.Subscribe()
	b, cancelB := h.Subscribe()
	defer cancelA()
	defer cancelB()

	h.Publish("alert.created", map[string]int{"id": 1})
	for _, ch := range []<-chan []byte{a, b} {
		var m Message
		if err := json.Unmarshal(<-ch, &m); err != nil || m.Type != "alert.created" {
			t.Fatalf("got %+v %v", m, err)
		}
	}
}

func TestSlowSubscriberDropped(t *testing.T) {
	h := New(nil)
	slow, cancel := h.Subscribe()
	defer cancel()
	for i := 0; i < BufferSize+1; i++ {
		h.Publish("x", i)
	}
	if h.Count() != 0 {
		t.Fatalf("slow subscriber still registered")
	}
	n := 0
	for range slow { // channel must be closed after draining
		n++
	}
	if n != BufferSize {
		t.Errorf("drained %d, want %d", n, BufferSize)
	}
}

func TestCancelIsIdempotent(t *testing.T) {
	h := New(nil)
	_, cancel := h.Subscribe()
	cancel()
	cancel()
	if h.Count() != 0 {
		t.Error("count != 0")
	}
}

func TestWebSocketEndToEnd(t *testing.T) {
	h := New(nil)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow() //nolint:errcheck

	read := func() Message {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m Message
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := read(); m.Type != "hello" {
		t.Fatalf("first message = %+v", m)
	}
	h.Publish("alert.updated", map[string]string{"state": "pulled"})
	m := read()
	if m.Type != "alert.updated" || m.Data.(map[string]any)["state"] != "pulled" {
		t.Errorf("got %+v", m)
	}

	c.Close(websocket.StatusNormalClosure, "bye") //nolint:errcheck
	deadline := time.Now().Add(2 * time.Second)
	for h.Count() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.Count() != 0 {
		t.Error("subscriber not removed after client closed")
	}
}

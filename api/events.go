package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event is one change (openapi Event): data is the resource's state now.
type Event struct {
	Type string `json:"type"` // ingest, job, apply, sync
	ID   string `json:"id"`
	Data any    `json:"data"`
}

// hub fans events out to every open GET /api/events stream. A slow reader
// misses events rather than slowing anyone down: each stream re-fetches
// what it needs when it reconnects.
type hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func newHub() *hub { return &hub{subs: map[chan Event]struct{}{}} }

func (h *hub) publish(e Event) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- e:
		default:
		}
	}
}

func (h *hub) subscribe() (chan Event, func()) {
	c := make(chan Event, 64)
	h.mu.Lock()
	h.subs[c] = struct{}{}
	h.mu.Unlock()
	return c, func() {
		h.mu.Lock()
		delete(h.subs, c)
		h.mu.Unlock()
	}
}

// keepalive is how often an idle stream gets a comment line, so proxies and
// the browser keep it open.
var keepalive = 15 * time.Second

// serve streams events until the client goes away. It's read with fetch
// (not EventSource) so the bearer header still applies.
func (h *hub) serve(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		problem(w, http.StatusInternalServerError, "Streaming unsupported", "")
		return
	}
	c, cancel := h.subscribe()
	defer cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": canon events\n\n")
	flusher.Flush()
	tick := time.NewTicker(keepalive)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			fmt.Fprint(w, ": ping\n\n")
		case e := <-c:
			b, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b)
		}
		flusher.Flush()
	}
}

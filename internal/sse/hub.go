// Package sse fans out realtime events to every open stream of a family.
package sse

import (
	"encoding/json"
	"sync"
	"time"
)

// Event types sent over /families/:id/stream.
const (
	DoseGiven       = "dose_given"
	DoseUndone      = "dose_undone"
	DoseSkipped     = "dose_skipped"
	LogCreated      = "log_created"
	AlertCreated    = "alert_created"
	AlertRead       = "alert_read"
	HandoverCreated = "handover_created"
	HandoverRead    = "handover_read"
	ShiftChanged    = "shift_changed"
	ProfileChanged  = "profile_changed"
	MedChanged      = "medication_changed"
)

type Event struct {
	Type      string    `json:"type"`
	ProfileID string    `json:"profile_id,omitempty"`
	ActorID   string    `json:"actor_id,omitempty"`
	Data      any       `json:"data,omitempty"`
	At        time.Time `json:"at"`
}

type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[chan []byte]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[string]map[chan []byte]struct{}{}} }

// Subscribe returns a channel of encoded events and an unsubscribe func.
func (h *Hub) Subscribe(familyID string) (<-chan []byte, func()) {
	ch := make(chan []byte, 32)
	h.mu.Lock()
	if h.subs[familyID] == nil {
		h.subs[familyID] = map[chan []byte]struct{}{}
	}
	h.subs[familyID][ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[familyID][ch]; ok {
			delete(h.subs[familyID], ch)
			close(ch)
		}
		if len(h.subs[familyID]) == 0 {
			delete(h.subs, familyID)
		}
		h.mu.Unlock()
	}
}

// Publish never blocks: slow clients drop events and resync on reconnect/refresh.
func (h *Hub) Publish(familyID string, ev Event) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for ch := range h.subs[familyID] {
		select {
		case ch <- b:
		default:
		}
	}
}

func (h *Hub) Count(familyID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[familyID])
}

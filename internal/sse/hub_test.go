package sse

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTwoClientsInSameFamilyReceiveDoseGiven(t *testing.T) {
	h := NewHub()
	a, unsubA := h.Subscribe("fam1")
	b, unsubB := h.Subscribe("fam1")
	other, unsubO := h.Subscribe("fam2")
	defer unsubA()
	defer unsubB()
	defer unsubO()

	h.Publish("fam1", Event{Type: DoseGiven, ProfileID: "p1"})

	for name, ch := range map[string]<-chan []byte{"a": a, "b": b} {
		select {
		case msg := <-ch:
			var ev Event
			if err := json.Unmarshal(msg, &ev); err != nil || ev.Type != DoseGiven {
				t.Fatalf("client %s got %s (%v)", name, msg, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("client %s did not receive event", name)
		}
	}
	select {
	case msg := <-other:
		t.Fatalf("other family must not receive events, got %s", msg)
	default:
	}
}

func TestUnsubscribeRemovesClient(t *testing.T) {
	h := NewHub()
	_, unsub := h.Subscribe("fam1")
	unsub()
	unsub() // idempotent
	if h.Count("fam1") != 0 {
		t.Fatal("expected no subscribers")
	}
	h.Publish("fam1", Event{Type: LogCreated}) // must not panic
}

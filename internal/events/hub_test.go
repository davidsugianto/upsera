package events

import (
	"testing"
	"time"
)

func TestPublishIsTeamScoped(t *testing.T) {
	h := NewHub()
	s1 := h.Subscribe(1)
	defer s1.Close()

	h.Publish(2, "team two")
	h.Publish(1, "team one")

	select {
	case v := <-s1.C:
		if v != "team one" {
			t.Fatalf("team 1 subscriber got %v", v)
		}
	case <-time.After(time.Second):
		t.Fatal("team 1 event not delivered")
	}
	select {
	case v := <-s1.C:
		t.Fatalf("unexpected extra event %v", v)
	default:
	}
}

func TestLaggingSubscriberIsDroppedWithoutBlocking(t *testing.T) {
	h := NewHub()
	slow := h.Subscribe(1)
	fast := h.Subscribe(1)
	defer fast.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range subscriberBuffer + 1 {
			h.Publish(1, i)
			<-fast.C // keep the other subscriber drained
		}
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Publish blocked on a lagging subscriber")
	}

	n := 0
	for range slow.C {
		n++
	}
	if n != subscriberBuffer {
		t.Fatalf("lagging subscriber received %d events before close, want %d", n, subscriberBuffer)
	}
	slow.Close() // idempotent after the hub closed it

	h.Publish(1, "still delivered")
	if v := <-fast.C; v != "still delivered" {
		t.Fatalf("healthy subscriber got %v", v)
	}
}

func TestCloseClosesSubscriptions(t *testing.T) {
	h := NewHub()
	a, b := h.Subscribe(1), h.Subscribe(2)
	h.Close()
	for _, s := range []*Subscription{a, b} {
		if _, ok := <-s.C; ok {
			t.Fatal("subscription channel still open after hub Close")
		}
	}
	h.Publish(1, "ignored") // no-op, must not panic

	late := h.Subscribe(1)
	if _, ok := <-late.C; ok {
		t.Fatal("Subscribe after Close returned an open channel")
	}
	late.Close()
}

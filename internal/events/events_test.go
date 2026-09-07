package events

import "testing"

func TestPublishReachesEverySubscriber(t *testing.T) {
	b := New()

	first, cancelFirst := b.Subscribe()
	second, cancelSecond := b.Subscribe()
	defer cancelFirst()
	defer cancelSecond()

	b.Publish(Event{Type: MessageStored, MessageID: "abc"})

	for i, ch := range []<-chan Event{first, second} {
		got := <-ch
		if got.MessageID != "abc" {
			t.Errorf("subscriber %d got %+v", i, got)
		}
	}
}

func TestCancelStopsDelivery(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe()

	cancel()
	if n := b.Subscribers(); n != 0 {
		t.Fatalf("Subscribers() = %d after cancel, want 0", n)
	}

	b.Publish(Event{Type: MessageStored})

	if _, open := <-ch; open {
		t.Error("received an event on a cancelled subscription")
	}

	// A second cancel must not panic on an already-closed channel; the
	// WebSocket handler cancels on both its normal and its error path.
	cancel()
}

func TestPublishDoesNotBlockOnAStalledSubscriber(t *testing.T) {
	b := New()
	_, cancel := b.Subscribe()
	defer cancel()

	// Nothing reads, so everything past the buffer is dropped. The point is
	// that Publish returns regardless -- SMTP ingest calls it inline.
	for i := 0; i < buffer*4; i++ {
		b.Publish(Event{Type: MessageStored})
	}
}

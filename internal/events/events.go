// Package events is the in-process fan-out between whatever changes the
// mailbox and whoever is watching it.
//
// The broker is deliberately lossy. A browser tab that stops reading must
// never be able to block SMTP ingest, so a subscriber whose buffer is full
// has events dropped rather than the publisher stalled. Clients treat the
// stream as a hint to refresh, not as a ledger they must see every entry of.
package events

import "sync"

// Event types. Each names something that happened, not something to do.
const (
	MessageStored       = "message.stored"
	MessageDeleted      = "message.deleted"
	ConversationDeleted = "conversation.deleted"
	ConversationRead    = "conversation.read"
	DeliveryCompleted   = "delivery.completed"
	ConfigChanged       = "config.changed"
	MailboxCleared      = "mailbox.cleared"
)

// Event is one notification. The fields carry enough for a client to update
// its list in place; anything richer is fetched over REST.
type Event struct {
	Type string `json:"type"`

	ConversationID int64  `json:"conversation_id,omitempty"`
	MessageID      string `json:"message_id,omitempty"`
	DeliveryID     int64  `json:"delivery_id,omitempty"`

	// Inbound distinguishes captured mail from Mailman's own replies, so
	// the UI can chime for one and stay quiet for the other.
	Inbound bool `json:"inbound,omitempty"`

	// Unread is the mailbox-wide count after the change, so the tab badge
	// never needs a follow-up request.
	Unread int `json:"unread"`

	// OK reports the outcome of a delivery.completed event.
	OK bool `json:"ok,omitempty"`
}

// buffer is how far behind a subscriber may fall before it starts losing
// events. Large enough to absorb a burst of captured mail, small enough that
// a dead connection cannot pin much memory.
const buffer = 32

// Broker fans events out to every subscriber.
type Broker struct {
	mu          sync.Mutex
	subscribers map[int]chan Event
	nextID      int
}

// New returns an empty broker.
func New() *Broker {
	return &Broker{subscribers: make(map[int]chan Event)}
}

// Subscribe returns a channel of events and the function that stops it. The
// cancel is safe to call more than once, which matters because the WebSocket
// handler defers it and may also hit it on an error path.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++

	ch := make(chan Event, buffer)
	b.subscribers[id] = ch

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if existing, ok := b.subscribers[id]; ok {
				delete(b.subscribers, id)
				close(existing)
			}
		})
	}

	return ch, cancel
}

// Publish delivers an event to everyone currently subscribed. It never
// blocks: a subscriber that is not keeping up simply misses this one.
func (b *Broker) Publish(event Event) {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, ch := range b.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

// Subscribers reports how many listeners are attached. Used by tests and the
// health endpoint.
func (b *Broker) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subscribers)
}

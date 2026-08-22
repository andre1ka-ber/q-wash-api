// Package eventbus is a tiny in-memory pub/sub used to wake up SSE
// subscribers when something about a washing point's live queue changes
// (a booking created, canceled, or advanced a status). It has no
// persistence or delivery guarantees — a dropped/restarted process loses
// subscribers — which is fine here: it's a "push a refresh sooner" signal,
// not a source of truth. Clients still refetch on reconnect/foreground.
package eventbus

import (
	"sync"

	"github.com/google/uuid"
)

type Bus struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[chan struct{}]struct{}
}

func New() *Bus {
	return &Bus{subs: make(map[uuid.UUID]map[chan struct{}]struct{})}
}

// Publish wakes every current subscriber of washingPointID. Non-blocking:
// a subscriber that hasn't drained its previous wakeup yet just coalesces
// into the same pending signal instead of blocking the publisher.
func (b *Bus) Publish(washingPointID uuid.UUID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[washingPointID] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Subscribe registers a new listener for washingPointID. Call unsubscribe
// when done (e.g. via defer) to avoid leaking the channel and map entry.
func (b *Bus) Subscribe(washingPointID uuid.UUID) (ch <-chan struct{}, unsubscribe func()) {
	c := make(chan struct{}, 1)

	b.mu.Lock()
	if b.subs[washingPointID] == nil {
		b.subs[washingPointID] = make(map[chan struct{}]struct{})
	}
	b.subs[washingPointID][c] = struct{}{}
	b.mu.Unlock()

	return c, func() {
		b.mu.Lock()
		delete(b.subs[washingPointID], c)
		if len(b.subs[washingPointID]) == 0 {
			delete(b.subs, washingPointID)
		}
		b.mu.Unlock()
	}
}

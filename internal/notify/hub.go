// Package notify delivers device state changes to live HTTP subscribers
// in this process. Events are not stored. A client that was disconnected
// does not receive what it missed, which matches the decision to skip
// replay after a restart.
package notify

import "sync"

// subscriberBuffer is small on purpose. Publish never blocks on a slow
// client: once a subscriber's buffer is full, that event is dropped for
// them and the request that saved the device continues.
const subscriberBuffer = 8

// StateChanged is the payload sent to subscribers.
type StateChanged struct {
	DeviceID      string `json:"device_id"`
	PreviousState string `json:"previous_state"`
	NewState      string `json:"new_state"`
}

// Hub is an in-process fan-out. A second API process would not see these
// events; nothing here crosses a network.
type Hub struct {
	mu   sync.Mutex
	subs map[chan StateChanged]struct{}
}

// NewHub returns a hub with no subscribers.
func NewHub() *Hub {
	return &Hub{subs: make(map[chan StateChanged]struct{})}
}

// Subscribe registers a subscriber. The returned function removes it and is
// safe to call more than once. The channel is not closed: the HTTP handler
// stops reading when the request context ends.
func (h *Hub) Subscribe() (<-chan StateChanged, func()) {
	ch := make(chan StateChanged, subscriberBuffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
		})
	}
	return ch, cancel
}

// Publish sends event to every current subscriber without blocking.
func (h *Hub) Publish(event StateChanged) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- event:
		default:
		}
	}
}

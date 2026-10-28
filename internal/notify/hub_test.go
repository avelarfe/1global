package notify

import (
	"strconv"
	"testing"
	"time"
)

func TestPublishReachesEverySubscriber(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	first, cancelFirst := hub.Subscribe()
	second, cancelSecond := hub.Subscribe()
	defer cancelFirst()
	defer cancelSecond()

	want := StateChanged{DeviceID: "device-1", PreviousState: "available", NewState: "in-use"}
	hub.Publish(want)

	assertReceived(t, first, want)
	assertReceived(t, second, want)
}

func TestUnsubscribeStopsDelivery(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	left, cancelLeft := hub.Subscribe()
	right, cancelRight := hub.Subscribe()
	defer cancelRight()
	cancelLeft()
	cancelLeft()

	want := StateChanged{DeviceID: "device-1", PreviousState: "available", NewState: "inactive"}
	hub.Publish(want)

	select {
	case got := <-left:
		t.Fatalf("removed subscriber received %+v", got)
	case <-time.After(50 * time.Millisecond):
	}
	assertReceived(t, right, want)
	if len(hub.subs) != 1 {
		t.Fatalf("subscribers = %d, want 1", len(hub.subs))
	}
}

func TestPublishDoesNotBlockWhenBufferIsFull(t *testing.T) {
	t.Parallel()
	hub := NewHub()
	ch, cancel := hub.Subscribe()
	defer cancel()

	for i := 0; i < subscriberBuffer+5; i++ {
		done := make(chan struct{})
		go func(n int) {
			hub.Publish(StateChanged{DeviceID: strconv.Itoa(n)})
			close(done)
		}(i)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("publish %d blocked", i)
		}
	}

	got := 0
	for {
		select {
		case <-ch:
			got++
		default:
			if got != subscriberBuffer {
				t.Fatalf("delivered %d events, want %d", got, subscriberBuffer)
			}
			return
		}
	}
}

func TestPublishWithNoSubscribers(t *testing.T) {
	t.Parallel()
	NewHub().Publish(StateChanged{DeviceID: "device-1"})
}

func assertReceived(t *testing.T, ch <-chan StateChanged, want StateChanged) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

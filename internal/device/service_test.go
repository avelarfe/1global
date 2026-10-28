package device

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/avelarfe/devices-api/internal/notify"
)

const sampleID = "11111111-1111-4111-8111-111111111111"

func TestCreateStoresDeviceAndDoesNotNotify(t *testing.T) {
	t.Parallel()
	store := newMem()
	svc, rec := newTestService(store)

	got, err := svc.Create(context.Background(), CreateInput{
		Name:  "  Pixel 8  ",
		Brand: " Google ",
		State: "available",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != sampleID || got.Name != "Pixel 8" || got.Brand != "Google" || got.State != StateAvailable {
		t.Fatalf("created %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("created_at was not assigned by the store")
	}
	if len(rec.snapshot()) != 0 {
		t.Fatal("create published a state change")
	}
}

func TestCreateValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   CreateInput
		want string
	}{
		{name: "missing name", in: CreateInput{Brand: "Google", State: "available"}, want: errNameRequired},
		{name: "blank name", in: CreateInput{Name: "  ", Brand: "Google", State: "available"}, want: errNameRequired},
		{name: "missing brand", in: CreateInput{Name: "Pixel 8", State: "available"}, want: errBrandRequired},
		{name: "missing state", in: CreateInput{Name: "Pixel 8", Brand: "Google"}, want: errStateInvalid},
		{name: "bad state", in: CreateInput{Name: "Pixel 8", Brand: "Google", State: "broken"}, want: errStateInvalid},
		{name: "long name", in: CreateInput{Name: strings.Repeat("a", 256), Brand: "Google", State: "available"}, want: errNameTooLong},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newMem()
			svc, rec := newTestService(store)
			_, err := svc.Create(context.Background(), test.in)
			assertValidation(t, err, test.want)
			if len(store.devices) != 0 {
				t.Fatal("invalid create wrote a row")
			}
			if len(rec.snapshot()) != 0 {
				t.Fatal("invalid create published")
			}
		})
	}
}

func TestCreateAllows255Runes(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(newMem())
	name := strings.Repeat("á", 255)
	got, err := svc.Create(context.Background(), CreateInput{Name: name, Brand: "Google", State: "inactive"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != name {
		t.Fatalf("name length = %d", len([]rune(got.Name)))
	}
}

func TestInUseRules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("rename is a conflict and does not write", func(t *testing.T) {
		t.Parallel()
		current := sampleDevice()
		current.State = StateInUse
		store := newMem(current)
		svc, rec := newTestService(store)

		_, err := svc.Replace(ctx, current.ID, ReplaceInput{Name: "Other", Brand: current.Brand, State: string(StateInUse)})
		assertConflict(t, err, errInUseRename)
		if store.updates != 0 || store.commits != 0 || store.deletes != 0 {
			t.Fatalf("updates=%d commits=%d deletes=%d", store.updates, store.commits, store.deletes)
		}
		if len(rec.snapshot()) != 0 {
			t.Fatal("conflict published")
		}
		got, err := store.Get(ctx, current.ID)
		if err != nil || got.Name != current.Name {
			t.Fatalf("stored %+v err=%v", got, err)
		}
	})

	t.Run("same name and brand can change state", func(t *testing.T) {
		t.Parallel()
		current := sampleDevice()
		current.State = StateInUse
		store := newMem(current)
		svc, rec := newTestService(store)

		got, err := svc.Patch(ctx, current.ID, PatchInput{State: ptr("inactive")})
		if err != nil {
			t.Fatal(err)
		}
		if got.State != StateInactive || got.Name != current.Name || !got.CreatedAt.Equal(current.CreatedAt) || got.ID != current.ID {
			t.Fatalf("updated %+v", got)
		}
		events := rec.snapshot()
		if len(events) != 1 {
			t.Fatalf("events = %+v", events)
		}
		want := notify.StateChanged{DeviceID: current.ID, PreviousState: "in-use", NewState: "inactive"}
		if events[0] != want {
			t.Fatalf("event = %+v", events[0])
		}
	})

	t.Run("delete is a conflict", func(t *testing.T) {
		t.Parallel()
		current := sampleDevice()
		current.State = StateInUse
		store := newMem(current)
		svc, rec := newTestService(store)

		err := svc.Delete(ctx, current.ID)
		assertConflict(t, err, errInUseDelete)
		if store.deletes != 0 {
			t.Fatal("delete was issued")
		}
		if _, err := store.Get(ctx, current.ID); err != nil {
			t.Fatal(err)
		}
		if len(rec.snapshot()) != 0 {
			t.Fatal("delete published")
		}
	})
}

func TestPartialUpdateAndNoop(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	current := sampleDevice()
	store := newMem(current)
	svc, rec := newTestService(store)

	got, err := svc.Patch(ctx, current.ID, PatchInput{Name: ptr("Pixel 9")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Pixel 9" || got.Brand != current.Brand || got.State != current.State || !got.CreatedAt.Equal(current.CreatedAt) {
		t.Fatalf("patched %+v", got)
	}
	if len(rec.snapshot()) != 0 {
		t.Fatal("name change published a state change")
	}

	beforeUpdates := store.updates
	got, err = svc.Patch(ctx, current.ID, PatchInput{Name: ptr("Pixel 9")})
	if err != nil {
		t.Fatal(err)
	}
	if store.updates != beforeUpdates || store.commits != 1 {
		t.Fatalf("noop wrote again: updates=%d commits=%d", store.updates, store.commits)
	}
	if len(rec.snapshot()) != 0 {
		t.Fatal("noop published")
	}
	if got.Name != "Pixel 9" {
		t.Fatalf("noop result %+v", got)
	}
}

func TestReplaceRequiresFields(t *testing.T) {
	t.Parallel()
	current := sampleDevice()
	svc, _ := newTestService(newMem(current))
	_, err := svc.Replace(context.Background(), current.ID, ReplaceInput{Name: "Pixel 8", Brand: "Google"})
	assertValidation(t, err, errStateInvalid)
}

func TestPatchRequiresAField(t *testing.T) {
	t.Parallel()
	current := sampleDevice()
	store := newMem(current)
	svc, _ := newTestService(store)
	_, err := svc.Patch(context.Background(), current.ID, PatchInput{})
	assertValidation(t, err, errPatchEmpty)
	if store.updates != 0 {
		t.Fatal("empty patch wrote")
	}
}

func TestFailedWriteDoesNotNotify(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	current := sampleDevice()

	t.Run("update error", func(t *testing.T) {
		t.Parallel()
		store := newMem(current)
		store.updateErr = errors.New("disk full")
		svc, rec := newTestService(store)
		_, err := svc.Replace(ctx, current.ID, ReplaceInput{Name: current.Name, Brand: current.Brand, State: "inactive"})
		if err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("err = %v", err)
		}
		if store.commits != 0 || len(rec.snapshot()) != 0 {
			t.Fatalf("commits=%d events=%d", store.commits, len(rec.snapshot()))
		}
		got, _ := store.Get(ctx, current.ID)
		if got.State != StateAvailable {
			t.Fatalf("state = %s", got.State)
		}
	})

	t.Run("commit error", func(t *testing.T) {
		t.Parallel()
		store := newMem(current)
		store.commitErr = errors.New("commit failed")
		svc, rec := newTestService(store)
		_, err := svc.Replace(ctx, current.ID, ReplaceInput{Name: current.Name, Brand: current.Brand, State: "inactive"})
		if err == nil || !strings.Contains(err.Error(), "commit failed") {
			t.Fatalf("err = %v", err)
		}
		if len(rec.snapshot()) != 0 {
			t.Fatal("published after a failed commit")
		}
		got, _ := store.Get(ctx, current.ID)
		if got.State != StateAvailable {
			t.Fatalf("state persisted as %s", got.State)
		}
	})
}

func TestDeleteAvailableDoesNotNotify(t *testing.T) {
	t.Parallel()
	current := sampleDevice()
	store := newMem(current)
	svc, rec := newTestService(store)
	if err := svc.Delete(context.Background(), current.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), current.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get err = %v", err)
	}
	if len(rec.snapshot()) != 0 {
		t.Fatal("delete published")
	}
}

func TestListFilters(t *testing.T) {
	t.Parallel()
	first := sampleDevice()
	second := sampleDevice()
	second.ID = "22222222-2222-4222-8222-222222222222"
	second.Brand = "Apple"
	second.State = StateInUse
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	svc, _ := newTestService(newMem(second, first))

	got, err := svc.List(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != first.ID || got[1].ID != second.ID {
		t.Fatalf("order = %+v", got)
	}

	got, err = svc.List(context.Background(), ptr("Google"), ptr("available"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("filtered = %+v", got)
	}

	_, err = svc.List(context.Background(), nil, ptr("nope"))
	assertValidation(t, err, errStateInvalid)
	_, err = svc.List(context.Background(), ptr("   "), nil)
	assertValidation(t, err, errBrandFilterEmpty)

	empty, err := svc.List(context.Background(), ptr("Missing"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty list = %#v", empty)
	}
}

func TestGetNotFoundAndInvalidID(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(newMem())
	_, err := svc.Get(context.Background(), sampleID)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	_, err = svc.Get(context.Background(), "events")
	assertValidation(t, err, errInvalidID)
	err = svc.Delete(context.Background(), "events")
	assertValidation(t, err, errInvalidID)
}

func sampleDevice() Device {
	return Device{
		ID:        sampleID,
		Name:      "Pixel 8",
		Brand:     "Google",
		State:     StateAvailable,
		CreatedAt: time.Date(2026, 10, 27, 15, 4, 5, 123456000, time.UTC),
	}
}

func newTestService(store *memStore) (*Service, *recorder) {
	rec := &recorder{}
	svc := NewService(store, rec.publish, func() (string, error) {
		return sampleID, nil
	})
	return svc, rec
}

type recorder struct {
	mu     sync.Mutex
	events []notify.StateChanged
}

func (r *recorder) publish(event notify.StateChanged) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *recorder) snapshot() []notify.StateChanged {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]notify.StateChanged, len(r.events))
	copy(out, r.events)
	return out
}

func ptr(value string) *string { return &value }

func assertValidation(t *testing.T, err error, message string) {
	t.Helper()
	var got ValidationError
	if !errors.As(err, &got) || got.Message != message {
		t.Fatalf("err = %v, want validation %q", err, message)
	}
}

func assertConflict(t *testing.T, err error, message string) {
	t.Helper()
	var got ConflictError
	if !errors.As(err, &got) || got.Message != message {
		t.Fatalf("err = %v, want conflict %q", err, message)
	}
}

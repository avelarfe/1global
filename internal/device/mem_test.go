package device

import (
	"context"
	"sort"
	"sync"
	"time"
)

// memStore is an in-memory Store for unit tests. It is not the production database.
type memStore struct {
	mu        sync.Mutex
	devices   map[string]Device
	insertErr error
	updateErr error
	commitErr error
	updates   int
	deletes   int
	commits   int
}

func newMem(devices ...Device) *memStore {
	store := &memStore{devices: map[string]Device{}}
	for _, d := range devices {
		store.devices[d.ID] = d
	}
	return store
}

func (m *memStore) Insert(_ context.Context, d Device) (Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.insertErr != nil {
		return Device{}, m.insertErr
	}
	if d.CreatedAt.IsZero() {
		// The MySQL repository reads this value back after INSERT. Tests pin it.
		d.CreatedAt = time.Date(2026, 10, 27, 15, 4, 5, 123456000, time.UTC)
	}
	m.devices[d.ID] = d
	return d, nil
}

func (m *memStore) Get(_ context.Context, id string) (Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.devices[id]
	if !ok {
		return Device{}, ErrNotFound
	}
	return d, nil
}

func (m *memStore) List(_ context.Context, brand, state *string) ([]Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Device{}
	for _, d := range m.devices {
		if brand != nil && d.Brand != *brand {
			continue
		}
		if state != nil && string(d.State) != *state {
			continue
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (m *memStore) Begin(_ context.Context) (Tx, error) {
	return &memTx{store: m}, nil
}

type memTx struct {
	store    *memStore
	pending  *Device
	deleteID string
	finished bool
}

func (t *memTx) GetForUpdate(_ context.Context, id string) (Device, error) {
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	d, ok := t.store.devices[id]
	if !ok {
		return Device{}, ErrNotFound
	}
	return d, nil
}

func (t *memTx) Update(_ context.Context, d Device) error {
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	t.store.updates++
	if t.store.updateErr != nil {
		return t.store.updateErr
	}
	copied := d
	t.pending = &copied
	t.deleteID = ""
	return nil
}

func (t *memTx) Delete(_ context.Context, id string) error {
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	t.store.deletes++
	t.deleteID = id
	t.pending = nil
	return nil
}

func (t *memTx) Commit() error {
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	if t.finished {
		return nil
	}
	t.finished = true
	t.store.commits++
	if t.store.commitErr != nil {
		return t.store.commitErr
	}
	if t.pending != nil {
		t.store.devices[t.pending.ID] = *t.pending
	}
	if t.deleteID != "" {
		delete(t.store.devices, t.deleteID)
	}
	return nil
}

func (t *memTx) Rollback() error {
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	if t.finished {
		return nil
	}
	t.finished = true
	t.pending = nil
	t.deleteID = ""
	return nil
}

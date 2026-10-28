package device

import "context"

// Store is the persistence port. The service owns the rules; Store only
// reads and writes rows.
type Store interface {
	Insert(ctx context.Context, d Device) (Device, error)
	Get(ctx context.Context, id string) (Device, error)
	List(ctx context.Context, brand, state *string) ([]Device, error)
	Begin(ctx context.Context) (Tx, error)
}

// Tx is a transaction that holds a row lock across the rule check and the write.
type Tx interface {
	GetForUpdate(ctx context.Context, id string) (Device, error)
	Update(ctx context.Context, d Device) error
	Delete(ctx context.Context, id string) error
	Commit() error
	Rollback() error
}

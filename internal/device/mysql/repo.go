// Package mysql stores devices in MySQL.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/avelarfe/devices-api/internal/device"
)

const deviceColumns = "id, name, brand, state, created_at"

// insertSQL leaves created_at to the database default.
const insertSQL = `INSERT INTO devices (id, name, brand, state) VALUES (?, ?, ?, ?)`

// updateSQL does not mention created_at. The column cannot be changed through this statement.
const updateSQL = `UPDATE devices SET name = ?, brand = ?, state = ? WHERE id = ?`

const deleteSQL = `DELETE FROM devices WHERE id = ?`

const selectByIDSQL = `SELECT ` + deviceColumns + ` FROM devices WHERE id = ?`

const selectForUpdateSQL = selectByIDSQL + ` FOR UPDATE`

// Repository is a database/sql store.
type Repository struct {
	db *sql.DB
}

// NewRepository returns a store that uses db.
func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// Insert writes a device and reads it back so created_at is the database value.
// Both statements share a transaction: a failed read does not leave a row behind.
func (r *Repository) Insert(ctx context.Context, d device.Device) (device.Device, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return device.Device{}, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, insertSQL, d.ID, d.Name, d.Brand, string(d.State)); err != nil {
		return device.Device{}, err
	}
	stored, err := scanDevice(tx.QueryRowContext(ctx, selectByIDSQL, d.ID))
	if err != nil {
		return device.Device{}, err
	}
	if err := tx.Commit(); err != nil {
		return device.Device{}, err
	}
	return stored, nil
}

// Get reads one device.
func (r *Repository) Get(ctx context.Context, id string) (device.Device, error) {
	d, err := scanDevice(r.db.QueryRowContext(ctx, selectByIDSQL, id))
	if errors.Is(err, sql.ErrNoRows) {
		return device.Device{}, device.ErrNotFound
	}
	return d, err
}

// List reads devices matching the optional exact filters.
func (r *Repository) List(ctx context.Context, brand, state *string) ([]device.Device, error) {
	query, args := listQuery(brand, state)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []device.Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Begin starts a transaction.
func (r *Repository) Begin(ctx context.Context) (device.Tx, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqlTx{tx: tx}, nil
}

type sqlTx struct {
	tx *sql.Tx
}

func (t *sqlTx) GetForUpdate(ctx context.Context, id string) (device.Device, error) {
	d, err := scanDevice(t.tx.QueryRowContext(ctx, selectForUpdateSQL, id))
	if errors.Is(err, sql.ErrNoRows) {
		return device.Device{}, device.ErrNotFound
	}
	return d, err
}

func (t *sqlTx) Update(ctx context.Context, d device.Device) error {
	result, err := t.tx.ExecContext(ctx, updateSQL, d.Name, d.Brand, string(d.State), d.ID)
	if err != nil {
		return err
	}
	return rowsAffected(result)
}

func (t *sqlTx) Delete(ctx context.Context, id string) error {
	result, err := t.tx.ExecContext(ctx, deleteSQL, id)
	if err != nil {
		return err
	}
	return rowsAffected(result)
}

func (t *sqlTx) Commit() error { return t.tx.Commit() }

func (t *sqlTx) Rollback() error {
	err := t.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

func rowsAffected(result sql.Result) error {
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return device.ErrNotFound
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDevice(row scanner) (device.Device, error) {
	var (
		d     device.Device
		state string
	)
	if err := row.Scan(&d.ID, &d.Name, &d.Brand, &state, &d.CreatedAt); err != nil {
		return device.Device{}, err
	}
	d.State = device.State(state)
	d.CreatedAt = d.CreatedAt.UTC()
	return d, nil
}

func listQuery(brand, state *string) (string, []any) {
	query := "SELECT " + deviceColumns + " FROM devices"
	var (
		filters []string
		args    []any
	)
	if brand != nil {
		filters = append(filters, "brand = ?")
		args = append(args, *brand)
	}
	if state != nil {
		filters = append(filters, "state = ?")
		args = append(args, *state)
	}
	if len(filters) > 0 {
		query += " WHERE " + strings.Join(filters, " AND ")
	}
	query += " ORDER BY created_at ASC, id ASC"
	return query, args
}

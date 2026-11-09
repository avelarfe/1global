package mysql

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	driver "github.com/go-sql-driver/mysql"

	"github.com/avelarfe/devices-api/internal/device"
)

// deviceColumns lists the columns in the order scanDevice expects. Every read
// returns the serial number so responses and updates can echo the stored value.
const deviceColumns = "id, name, brand, serial_number, state, created_at"

// insertSQL is a plain INSERT. It relies on uq_devices_serial_number to reject
// a duplicate serial number; doing a SELECT first would leave a window where
// two concurrent creates both see no row and both try to insert.
const insertSQL = `INSERT INTO devices (id, name, brand, serial_number, state) VALUES (?, ?, ?, ?, ?)`

// updateSQL does not mention created_at or serial_number. Those columns cannot
// be changed through this statement.
const updateSQL = `UPDATE devices SET name = ?, brand = ?, state = ? WHERE id = ?`

const deleteSQL = `DELETE FROM devices WHERE id = ?`

const selectByIDSQL = `SELECT ` + deviceColumns + ` FROM devices WHERE id = ?`

const selectForUpdateSQL = selectByIDSQL + ` FOR UPDATE`

type Repository struct {
	db *sql.DB
}

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

	if _, err := tx.ExecContext(ctx, insertSQL, d.ID, d.Name, d.Brand, d.SerialNumber, string(d.State)); err != nil {
		if isDuplicateSerial(err) {
			return device.Device{}, device.ErrDuplicateSerial
		}
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

func (r *Repository) Get(ctx context.Context, id string) (device.Device, error) {
	d, err := scanDevice(r.db.QueryRowContext(ctx, selectByIDSQL, id))
	if errors.Is(err, sql.ErrNoRows) {
		return device.Device{}, device.ErrNotFound
	}
	return d, err
}

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

// Update writes only the mutable columns. serial_number and created_at are
// absent from updateSQL, so no caller can change them through this path.
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

// scanDevice reads a row in deviceColumns order, including serial_number.
func scanDevice(row scanner) (device.Device, error) {
	var (
		d     device.Device
		state string
	)
	if err := row.Scan(&d.ID, &d.Name, &d.Brand, &d.SerialNumber, &state, &d.CreatedAt); err != nil {
		return device.Device{}, err
	}
	d.State = device.State(state)
	d.CreatedAt = d.CreatedAt.UTC()
	return d, nil
}

// isDuplicateSerial identifies the unique-index violation for serial_number.
// MySQL reports every duplicate key as error 1062, so the key name in the
// message distinguishes a serial collision from, for example, an id collision.
// Other duplicate-key errors and other MySQL errors stay internal errors.
func isDuplicateSerial(err error) bool {
	var mysqlErr *driver.MySQLError
	return errors.As(err, &mysqlErr) &&
		mysqlErr.Number == 1062 &&
		strings.Contains(mysqlErr.Message, "uq_devices_serial_number")
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

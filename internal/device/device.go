// Package device is the HTTP and domain layer for the devices resource.
package device

import (
	"encoding/json"
	"errors"
	"time"
	"unicode/utf8"
)

const (
	errNameRequired     = "name is required"
	errNameTooLong      = "name must be at most 255 characters"
	errBrandRequired    = "brand is required"
	errBrandTooLong     = "brand must be at most 255 characters"
	errSerialRequired   = "serial_number is required"
	errSerialTooLong    = "serial_number must be at most 255 characters"
	errSerialExists     = "device with this serial number already exists"
	errStateInvalid     = "state must be available, in-use, or inactive"
	errPatchEmpty       = "at least one of name, brand, or state is required"
	errInUseRename      = "name and brand cannot be updated while the device is in use"
	errInUseDelete      = "in-use devices cannot be deleted"
	errInvalidID        = "invalid device id"
	errBrandFilterEmpty = "brand filter cannot be empty"
	errNotFoundMsg      = "device not found"
	errInternal         = "internal error"

	maxFieldRunes = 255
)

var ErrNotFound = errors.New(errNotFoundMsg)

// ErrDuplicateSerial is returned by the repository when the unique serial
// number index rejects an insert. The service turns it into a ConflictError.
var ErrDuplicateSerial = errors.New("duplicate serial number")

// ValidationError is a client mistake: bad JSON or a field that fails a rule.
type ValidationError struct {
	Message string
}

func (e ValidationError) Error() string { return e.Message }

// ConflictError is a domain rule the current row does not allow.
type ConflictError struct {
	Message string
}

func (e ConflictError) Error() string { return e.Message }

// State is the lifecycle of a device. Any state may follow any other state.
type State string

const (
	StateAvailable State = "available"
	StateInUse     State = "in-use"
	StateInactive  State = "inactive"
)

func (s State) Valid() bool {
	switch s {
	case StateAvailable, StateInUse, StateInactive:
		return true
	default:
		return false
	}
}

// Device is the stored resource. Creation time is assigned by MySQL.
type Device struct {
	ID    string
	Name  string
	Brand string
	// SerialNumber is the device's business identity. It is required on
	// create, unique across devices, and never changes after creation.
	SerialNumber string
	State        State
	CreatedAt    time.Time
}

type deviceJSON struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Brand        string `json:"brand"`
	SerialNumber string `json:"serial_number"`
	State        State  `json:"state"`
	CreatedAt    string `json:"created_at"`
}

// MarshalJSON formats created_at in UTC with microsecond precision, matching
// the DATETIME(6) column.
func (d Device) MarshalJSON() ([]byte, error) {
	return json.Marshal(deviceJSON{
		ID:           d.ID,
		Name:         d.Name,
		Brand:        d.Brand,
		SerialNumber: d.SerialNumber,
		State:        d.State,
		CreatedAt:    d.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000Z"),
	})
}

func validateName(name string) error {
	if name == "" {
		return ValidationError{Message: errNameRequired}
	}
	if utf8.RuneCountInString(name) > maxFieldRunes {
		return ValidationError{Message: errNameTooLong}
	}
	return nil
}

func validateBrand(brand string) error {
	if brand == "" {
		return ValidationError{Message: errBrandRequired}
	}
	if utf8.RuneCountInString(brand) > maxFieldRunes {
		return ValidationError{Message: errBrandTooLong}
	}
	return nil
}

// validateSerialNumber checks the already-trimmed serial number. Length is
// counted in Unicode characters, matching the VARCHAR(255) column and the
// other text fields. Uniqueness is deliberately absent here: a check-then-
// insert in Go would leave a race window between two creates. The database
// unique index is the only arbiter, and the repository maps its violation to
// ErrDuplicateSerial.
func validateSerialNumber(serialNumber string) error {
	if serialNumber == "" {
		return ValidationError{Message: errSerialRequired}
	}
	if utf8.RuneCountInString(serialNumber) > maxFieldRunes {
		return ValidationError{Message: errSerialTooLong}
	}
	return nil
}

func validateState(state State) error {
	if !state.Valid() {
		return ValidationError{Message: errStateInvalid}
	}
	return nil
}

// validateChange applies the in-use rule before the field checks, so a
// forbidden rename is a conflict even when the new value would also be invalid.
func validateChange(current, next Device) error {
	if current.State == StateInUse && (next.Name != current.Name || next.Brand != current.Brand) {
		return ConflictError{Message: errInUseRename}
	}
	if err := validateName(next.Name); err != nil {
		return err
	}
	if err := validateBrand(next.Brand); err != nil {
		return err
	}
	return validateState(next.State)
}

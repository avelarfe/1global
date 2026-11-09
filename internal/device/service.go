package device

import (
	"context"
	"errors"
	"strings"

	"github.com/avelarfe/devices-api/internal/id"
	"github.com/avelarfe/devices-api/internal/notify"
)

// CreateInput is the body of POST /devices. The server assigns id and
// created_at. SerialNumber is required and is the device's business identity;
// the database enforces that it is unique.
type CreateInput struct {
	Name         string `json:"name"`
	Brand        string `json:"brand"`
	SerialNumber string `json:"serial_number"`
	State        string `json:"state"`
}

// ReplaceInput is the body of PUT /devices/{id}. All three mutable fields are required.
type ReplaceInput struct {
	Name  string `json:"name"`
	Brand string `json:"brand"`
	State string `json:"state"`
}

// PatchInput is the body of PATCH /devices/{id}. A null or omitted mutable field is left unchanged.
type PatchInput struct {
	Name  *string `json:"name"`
	Brand *string `json:"brand"`
	State *string `json:"state"`
}

// Service applies device rules and publishes a state change only after commit.
type Service struct {
	repo    Store
	publish func(notify.StateChanged)
	newID   func() (string, error)
}

// A nil dependency is a startup bug, not a request error.
func NewService(repo Store, publish func(notify.StateChanged), newID func() (string, error)) *Service {
	if repo == nil || publish == nil || newID == nil {
		panic("device.NewService: nil dependency")
	}
	return &Service{repo: repo, publish: publish, newID: newID}
}

// Create validates and stores a new device. SerialNumber is trimmed like the
// other text fields. Two concurrent creates with the same serial cannot both
// succeed: the insert relies on the unique index, and the loser is reported as
// a conflict. Creation has no previous state, so it does not notify.
func (s *Service) Create(ctx context.Context, in CreateInput) (Device, error) {
	device := Device{
		Name:         strings.TrimSpace(in.Name),
		Brand:        strings.TrimSpace(in.Brand),
		SerialNumber: strings.TrimSpace(in.SerialNumber),
		State:        State(strings.TrimSpace(in.State)),
	}
	if err := validateName(device.Name); err != nil {
		return Device{}, err
	}
	if err := validateBrand(device.Brand); err != nil {
		return Device{}, err
	}
	if err := validateSerialNumber(device.SerialNumber); err != nil {
		return Device{}, err
	}
	if err := validateState(device.State); err != nil {
		return Device{}, err
	}
	newID, err := s.newID()
	if err != nil {
		return Device{}, err
	}
	device.ID = newID
	// There is no read-then-insert check. The unique serial-number index
	// arbitrates simultaneous creates, so the database decides the winner.
	stored, err := s.repo.Insert(ctx, device)
	if errors.Is(err, ErrDuplicateSerial) {
		return Device{}, ConflictError{Message: errSerialExists}
	}
	return stored, err
}

func (s *Service) Get(ctx context.Context, deviceID string) (Device, error) {
	if err := validateDeviceID(deviceID); err != nil {
		return Device{}, err
	}
	return s.repo.Get(ctx, deviceID)
}

// List returns every device, optionally filtered by exact brand and exact state.
// Both filters set means both must match. The result is never a nil slice.
func (s *Service) List(ctx context.Context, brand, state *string) ([]Device, error) {
	if brand != nil && strings.TrimSpace(*brand) == "" {
		return nil, ValidationError{Message: errBrandFilterEmpty}
	}
	if state != nil {
		if err := validateState(State(strings.TrimSpace(*state))); err != nil {
			return nil, err
		}
		trimmed := strings.TrimSpace(*state)
		state = &trimmed
	}
	devices, err := s.repo.List(ctx, brand, state)
	if err != nil {
		return nil, err
	}
	if devices == nil {
		devices = []Device{}
	}
	return devices, nil
}

// Replace overwrites name, brand, and state. Id, serial number, and creation
// time stay as stored; ReplaceInput has no fields for them.
func (s *Service) Replace(ctx context.Context, deviceID string, in ReplaceInput) (Device, error) {
	return s.mutate(ctx, deviceID, func(current Device) (Device, error) {
		next := current
		next.Name = strings.TrimSpace(in.Name)
		next.Brand = strings.TrimSpace(in.Brand)
		next.State = State(strings.TrimSpace(in.State))
		if err := validateChange(current, next); err != nil {
			return Device{}, err
		}
		return next, nil
	})
}

// Patch changes only the supplied mutable fields. Id, serial number, and
// creation time cannot be addressed because PatchInput does not contain them.
func (s *Service) Patch(ctx context.Context, deviceID string, in PatchInput) (Device, error) {
	if in.Name == nil && in.Brand == nil && in.State == nil {
		return Device{}, ValidationError{Message: errPatchEmpty}
	}
	return s.mutate(ctx, deviceID, func(current Device) (Device, error) {
		next := current
		if in.Name != nil {
			next.Name = strings.TrimSpace(*in.Name)
		}
		if in.Brand != nil {
			next.Brand = strings.TrimSpace(*in.Brand)
		}
		if in.State != nil {
			next.State = State(strings.TrimSpace(*in.State))
		}
		if err := validateChange(current, next); err != nil {
			return Device{}, err
		}
		return next, nil
	})
}

// Delete removes a device that is not in use. Deletion is not a state change,
// so subscribers are not notified.
func (s *Service) Delete(ctx context.Context, deviceID string) error {
	if err := validateDeviceID(deviceID); err != nil {
		return err
	}
	tx, err := s.repo.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	current, err := tx.GetForUpdate(ctx, deviceID)
	if err != nil {
		return err
	}
	if current.State == StateInUse {
		return ConflictError{Message: errInUseDelete}
	}
	if err := tx.Delete(ctx, deviceID); err != nil {
		return err
	}
	return tx.Commit()
}

// mutate locks the row, applies fn, and commits before telling subscribers.
// fn's id, serial number, and creation time are discarded so a caller cannot
// change them.
func (s *Service) mutate(ctx context.Context, deviceID string, fn func(Device) (Device, error)) (Device, error) {
	if err := validateDeviceID(deviceID); err != nil {
		return Device{}, err
	}
	tx, err := s.repo.Begin(ctx)
	if err != nil {
		return Device{}, err
	}
	defer tx.Rollback()

	current, err := tx.GetForUpdate(ctx, deviceID)
	if err != nil {
		return Device{}, err
	}
	next, err := fn(current)
	if err != nil {
		return Device{}, err
	}
	next.ID = current.ID
	next.SerialNumber = current.SerialNumber
	next.CreatedAt = current.CreatedAt

	if next.Name == current.Name && next.Brand == current.Brand && next.State == current.State {
		return current, nil
	}
	if err := tx.Update(ctx, next); err != nil {
		return Device{}, err
	}
	if err := tx.Commit(); err != nil {
		return Device{}, err
	}
	// The row is durable now. Publishing earlier would tell clients about a
	// state that might still roll back.
	if next.State != current.State {
		s.publish(notify.StateChanged{
			DeviceID:      current.ID,
			PreviousState: string(current.State),
			NewState:      string(next.State),
		})
	}
	return next, nil
}

func validateDeviceID(deviceID string) error {
	if err := id.Validate(deviceID); err != nil {
		return ValidationError{Message: errInvalidID}
	}
	return nil
}

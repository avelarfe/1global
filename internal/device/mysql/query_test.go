package mysql

import (
	"errors"
	"strings"
	"testing"

	driver "github.com/go-sql-driver/mysql"
)

func TestListQueryUsesPlaceholders(t *testing.T) {
	t.Parallel()
	brand := "O'Malley"
	state := "in-use"
	query, args := listQuery(&brand, &state)

	if strings.Contains(query, brand) || strings.Contains(query, state) {
		t.Fatalf("filter values were interpolated into SQL: %s", query)
	}
	if !strings.Contains(query, "brand = ?") || !strings.Contains(query, "state = ?") {
		t.Fatalf("query = %s", query)
	}
	if !strings.Contains(query, "ORDER BY created_at ASC, id ASC") {
		t.Fatalf("query = %s", query)
	}
	if len(args) != 2 || args[0] != brand || args[1] != state {
		t.Fatalf("args = %#v", args)
	}
}

func TestListQueryFilters(t *testing.T) {
	t.Parallel()
	brand := "Google"
	state := "available"

	query, args := listQuery(nil, nil)
	if strings.Contains(query, "WHERE") || len(args) != 0 {
		t.Fatalf("unfiltered query = %s args=%#v", query, args)
	}

	query, args = listQuery(&brand, nil)
	if strings.Contains(query, "state = ?") || len(args) != 1 || args[0] != brand {
		t.Fatalf("brand query = %s args=%#v", query, args)
	}

	query, args = listQuery(nil, &state)
	if strings.Contains(query, "brand = ?") || len(args) != 1 || args[0] != state {
		t.Fatalf("state query = %s args=%#v", query, args)
	}
}

func TestWritesDoNotTouchCreationTime(t *testing.T) {
	t.Parallel()
	if strings.Contains(strings.ToLower(insertSQL), "created_at") {
		t.Fatal("insert must leave created_at to the database default")
	}
	if strings.Contains(strings.ToLower(updateSQL), "created_at") {
		t.Fatal("update must not change created_at")
	}
	if !strings.Contains(selectByIDSQL, "created_at") {
		t.Fatal("reads must return created_at")
	}
}

func TestSerialNumberWrites(t *testing.T) {
	t.Parallel()
	if !strings.Contains(insertSQL, "serial_number") {
		t.Fatal("insert must store the serial number")
	}
	if strings.Contains(updateSQL, "serial_number") {
		t.Fatal("update must not change the serial number")
	}
	if !strings.Contains(selectByIDSQL, "serial_number") {
		t.Fatal("reads must return the serial number")
	}
}

func TestIsDuplicateSerial(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "serial number unique key",
			err:  &driver.MySQLError{Number: 1062, Message: "Duplicate entry 'SN-001' for key 'devices.uq_devices_serial_number'"},
			want: true,
		},
		{
			name: "other unique key",
			err:  &driver.MySQLError{Number: 1062, Message: "Duplicate entry 'id' for key 'PRIMARY'"},
			want: false,
		},
		{
			name: "other mysql error",
			err:  &driver.MySQLError{Number: 1064, Message: "syntax error"},
			want: false,
		},
		{
			name: "not a mysql error",
			err:  errors.New("duplicate entry"),
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := isDuplicateSerial(test.err); got != test.want {
				t.Fatalf("isDuplicateSerial(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

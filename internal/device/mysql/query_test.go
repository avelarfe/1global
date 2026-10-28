package mysql

import (
	"strings"
	"testing"
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

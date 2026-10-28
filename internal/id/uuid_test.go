package id

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestNewIsUUIDv4(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		got, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if err := Validate(got); err != nil {
			t.Fatalf("New produced %q: %v", got, err)
		}
		raw, err := hex.DecodeString(strings.ReplaceAll(got, "-", ""))
		if err != nil {
			t.Fatal(err)
		}
		if raw[6]>>4 != 4 {
			t.Fatalf("version nibble = %x", raw[6]>>4)
		}
		if raw[8]&0xc0 != 0x80 {
			t.Fatalf("variant bits = %08b", raw[8]&0xc0)
		}
		if _, ok := seen[got]; ok {
			t.Fatalf("duplicate id %s", got)
		}
		seen[got] = struct{}{}
	}
}

func TestValidateRejects(t *testing.T) {
	t.Parallel()
	samples := []string{
		"",
		"events",
		"11111111-1111-1111-8111-111111111111", // version 1
		"11111111-1111-4111-1111-111111111111", // wrong variant
		"11111111-1111-4111-8111-11111111111",  // too short
	}
	for _, sample := range samples {
		if err := Validate(sample); err == nil {
			t.Fatalf("Validate(%q) accepted an invalid id", sample)
		}
	}
}

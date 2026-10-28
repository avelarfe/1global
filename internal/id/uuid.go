// Package id generates and checks UUID version 4 identifiers.
// The standard library has no UUID type, and this service only needs the
// version-4 format, so the helper stays in-tree.
package id

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"regexp"
)

// version 4 in the third group, RFC 4122 variant in the fourth.
var uuidV4 = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// New returns a random UUID version 4.
func New() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(b[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

// Validate reports whether s is a canonical UUID version 4.
func Validate(s string) error {
	if !uuidV4.MatchString(s) {
		return errors.New("invalid uuid")
	}
	return nil
}

// Package shortcode makes and reads the short codes people type by hand:
// resume codes and exam codes.
package shortcode

import (
	"io"
	"strings"
)

// Alphabet has 30 symbols without look-alikes (no I, L, O, U, 0, 1).
const Alphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

// Random returns n symbols of Alphabet drawn from random. Bytes ≥ 240 are
// skipped so every symbol is equally likely.
func Random(random io.Reader, n int) (string, error) {
	code := make([]byte, 0, n)
	buf := make([]byte, 2*n)
	for len(code) < n {
		if _, err := io.ReadFull(random, buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b < 240 && len(code) < n {
				code = append(code, Alphabet[b%30])
			}
		}
	}
	return string(code), nil
}

// Normalize uppercases input and drops everything outside [A-Z0-9]. It
// reports whether the result is n symbols of Alphabet.
func Normalize(input string, n int) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToUpper(input) {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	code := b.String()
	if len(code) != n {
		return "", false
	}
	for _, r := range code {
		if !strings.ContainsRune(Alphabet, r) {
			return "", false
		}
	}
	return code, true
}

package shortcode

import (
	"bytes"
	"math/rand/v2"
	"strings"
	"testing"
)

func TestRandom_SkipsBiasedBytes(t *testing.T) {
	// 240..255 would favour the first 16 symbols, so they are skipped.
	src := append(bytes.Repeat([]byte{255, 240}, 4), 0, 29, 30, 239, 1, 59, 31, 28, 0, 0, 0, 0, 0, 0, 0, 0)
	got, err := Random(bytes.NewReader(src), 8)
	if err != nil {
		t.Fatal(err)
	}
	if want := "A9A9B9B8"; got != want {
		t.Fatalf("Random = %q, want %q", got, want)
	}
}

func TestRandom_LengthAndAlphabet(t *testing.T) {
	for _, n := range []int{6, 8} {
		random := rand.NewChaCha8([32]byte{7})
		seen := map[rune]bool{}
		for range 1000 {
			code, err := Random(random, n)
			if err != nil {
				t.Fatal(err)
			}
			if len(code) != n {
				t.Fatalf("Random(%d) = %q, length %d", n, code, len(code))
			}
			for _, r := range code {
				if !strings.ContainsRune(Alphabet, r) {
					t.Fatalf("Random(%d) = %q holds %q outside Alphabet", n, code, r)
				}
				seen[r] = true
			}
		}
		if len(seen) != len(Alphabet) {
			t.Fatalf("Random(%d) used %d of %d symbols", n, len(seen), len(Alphabet))
		}
	}
}

func TestRandom_ShortReaderFails(t *testing.T) {
	got, err := Random(bytes.NewReader([]byte{1, 2, 3}), 6)
	if err == nil {
		t.Fatalf("Random = %q, want an error", got)
	}
	if got != "" {
		t.Fatalf("Random = %q with error, want \"\"", got)
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		input string
		n     int
		want  string
		ok    bool
	}{
		{"abc-def", 6, "ABCDEF", true},
		{" ab c d-e f ", 6, "ABCDEF", true},
		{"ABCD-EFGH", 8, "ABCDEFGH", true},
		{"ABCDE", 6, "", false},
		{"ABCDEFG", 6, "", false},
		{"ABCDEO", 6, "", false},
		{"ABCDE1", 6, "", false},
		{"", 6, "", false},
		{"ABCDEF", 8, "", false},
	}
	for _, tt := range tests {
		got, ok := Normalize(tt.input, tt.n)
		if ok != tt.ok || (tt.ok && got != tt.want) {
			t.Errorf("Normalize(%q, %d) = %q, %v; want %q, %v", tt.input, tt.n, got, ok, tt.want, tt.ok)
		}
	}
}

func TestAlphabet_NoLookAlikes(t *testing.T) {
	if len(Alphabet) != 30 {
		t.Fatalf("len(Alphabet) = %d, want 30", len(Alphabet))
	}
	seen := map[rune]bool{}
	for _, r := range Alphabet {
		if seen[r] {
			t.Fatalf("Alphabet repeats %q", r)
		}
		seen[r] = true
	}
	if strings.ContainsAny(Alphabet, "ILOU01") {
		t.Fatalf("Alphabet %q holds a look-alike", Alphabet)
	}
}

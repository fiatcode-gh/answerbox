package attempt

import (
	"bytes"
	"testing"
)

func TestRandomCode_SkipsBiasedBytes(t *testing.T) {
	// 240..255 would favour the first 16 symbols, so they are skipped.
	src := append(bytes.Repeat([]byte{255, 240}, 4), 0, 29, 30, 239, 1, 59, 31, 28, 0, 0, 0, 0, 0, 0, 0, 0)
	got, err := randomCode(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	if want := "A9A9B9B8"; got != want {
		t.Fatalf("randomCode = %q, want %q", got, want)
	}
}

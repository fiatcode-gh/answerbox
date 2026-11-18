package attempt

import (
	"strings"
	"testing"
)

type matchCase struct {
	a, b string
	want bool
}

func matchTable() []matchCase {
	cases := []matchCase{
		// Text, match.
		{"Jakarta", "jakarta", true},
		{"  JAKARTA  ", "Jakarta", true},
		{"DKI   Jakarta", "dki jakarta", true},
		{"DKI\tJakarta", "DKI Jakarta", true},
		{"ÉCOLE", "école", true},
		// Text, no match.
		{"Jakarta.", "Jakarta", false},
		{"école", "ecole", false},
		{"Jakarta", "Jakarte", false},
		{"DKIJakarta", "DKI Jakarta", false},
		// Numbers, match.
		{"1.003", "1003", true},
		{"0,50", "0,5", true},
		{"007", "7", true},
		{"-1,5", "-1,50", true},
		{"12.345.678", "12345678", true},
		{"1.003,0", "1003", true},
		{"-0", "0", true},
		{" 1.003 ", "1003", true},
		// Numbers, no match.
		{"1,5", "1,05", false},
		{"-7", "7", false},
		{"1.003", "1.004", false},
		// Text forms against their numeric look-alikes.
		{"1.5", "1,5", false},
		{"1.00", "1", false},
		{"1.00", "100", false},
		{"1,003.5", "1,0035", false},
		{"1 003", "1003", false},
		{"+5", "5", false},
		{",5", "0,5", false},
		{"5,", "5", false},
		{"Rp 1.000", "Rp 1000", false},
		{"5 cm", "5cm", false},
		{"5  CM", "5 cm", true},
		// Number against text.
		{"1003", "1003 buah", false},
		{"1.5", "15", false},
		// Empty.
		{"", "", false},
		{"   ", "   ", false},
		{"", "   ", false},
		{"", "Jakarta", false},
		{"   ", "0", false},
	}
	for _, s := range []string{"1.5", "1.00", "1,003.5", "1 003", "+5", ",5", "5,", "Rp 1.000", "5 cm"} {
		cases = append(cases,
			matchCase{s, s, true},
			matchCase{s, strings.ToUpper(s), true},
		)
	}
	return cases
}

func TestMatches_ContractTable(t *testing.T) {
	for _, c := range matchTable() {
		if got := keyOf(c.a).matches(keyOf(c.b)); got != c.want {
			t.Errorf("match(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestMatches_BothDirections(t *testing.T) {
	for _, c := range matchTable() {
		ab := keyOf(c.a).matches(keyOf(c.b))
		ba := keyOf(c.b).matches(keyOf(c.a))
		if ab != ba {
			t.Errorf("match(%q, %q) = %v but match(%q, %q) = %v", c.a, c.b, ab, c.b, c.a, ba)
		}
	}
}

func TestKeyOf_Number(t *testing.T) {
	long := "1234567890123456789012345678901234567890"
	cases := []struct{ in, want string }{
		{"1.003", "1003"},
		{"0,50", "0,5"},
		{"007", "7"},
		{"-1,50", "-1,5"},
		{"0,0", "0"},
		{"-0,0", "0"},
		{"000.001", "1"},
		{long, long},
		{"1.5", ""},
		{"1.00", ""},
		{"1234.567", ""},
		{"1,003.5", ""},
		{"+5", ""},
		{",5", ""},
		{"5,", ""},
		{"1 003", ""},
		{"١٢٣", ""},
	}
	for _, c := range cases {
		if got := keyOf(c.in).number; got != c.want {
			t.Errorf("keyOf(%q).number = %q, want %q", c.in, got, c.want)
		}
	}
}

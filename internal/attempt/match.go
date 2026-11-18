package attempt

import (
	"regexp"
	"strings"
)

// numberPattern is a number in Indonesian notation: an optional minus, a
// whole part either plain or grouped by dots in threes, and an optional
// comma fraction. Go's \d is ASCII only; other digits are text.
var numberPattern = regexp.MustCompile(`^(-?)(\d+|\d{1,3}(?:\.\d{3})+)(?:,(\d+))?$`)

// answerKey is an answer prepared for matching. text is the answer trimmed
// with every run of white space as one space; number is its canonical value
// when it is a number in Indonesian notation, else "".
type answerKey struct{ text, number string }

func keyOf(answer string) answerKey {
	return answerKey{
		text:   strings.Join(strings.Fields(answer), " "),
		number: canonicalNumber(strings.TrimSpace(answer)),
	}
}

// canonicalNumber returns the canonical value of s, or "" when s is not a
// number in Indonesian notation.
func canonicalNumber(s string) string {
	m := numberPattern.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	whole := strings.TrimLeft(strings.ReplaceAll(m[2], ".", ""), "0")
	if whole == "" {
		whole = "0"
	}
	value := whole
	if frac := strings.TrimRight(m[3], "0"); frac != "" {
		value += "," + frac
	}
	if m[1] == "-" && value != "0" {
		value = "-" + value
	}
	return value
}

// matches reports whether two answers match: as numbers by value when both
// are numbers, else as text ignoring letter case. An empty answer matches
// nothing.
func (k answerKey) matches(o answerKey) bool {
	if k.text == "" || o.text == "" {
		return false
	}
	if k.number != "" && o.number != "" {
		return k.number == o.number
	}
	return strings.EqualFold(k.text, o.text)
}

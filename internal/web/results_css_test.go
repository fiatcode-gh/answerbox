package web

import (
	"slices"
	"strings"
	"testing"
)

// cssDeclares reports whether some rule of app.css whose selector passes
// match declares decl.
func cssDeclares(t *testing.T, match func(selector string) bool, decl string) bool {
	t.Helper()
	for _, r := range parseCSS(t, readStatic(t, "app.css")) {
		if match(r.selector) && slices.Contains(r.decls, decl) {
			return true
		}
	}
	return false
}

// View and Delete of a results row sit on one line (visual note "View and
// Delete stack in results rows").
func TestCSS_ResultsRowActionsOneLine(t *testing.T) {
	match := func(sel string) bool {
		return strings.Contains(sel, "#attempts") && strings.Contains(sel, ".row-actions")
	}
	if !cssDeclares(t, match, "white-space: nowrap") {
		t.Error("no rule for #attempts .row-actions declares white-space: nowrap")
	}
}

// Times and scores in the results table never break mid-value.
func TestCSS_ResultsCompactCells(t *testing.T) {
	match := func(sel string) bool {
		for _, s := range strings.Split(sel, ",") {
			if strings.TrimSpace(s) == "#attempts td.numeric" {
				return true
			}
		}
		return false
	}
	if !cssDeclares(t, match, "white-space: nowrap") {
		t.Error("no rule for #attempts td.numeric declares white-space: nowrap")
	}
}

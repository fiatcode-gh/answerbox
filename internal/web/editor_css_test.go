package web

import (
	"slices"
	"strings"
	"testing"
)

// The save row of an editor form is sticky from 641 px. The page keeps
// scroll padding at the bottom while that row is there, so the browser
// scrolls a focused control above the row instead of under it.
func TestCSS_SaveRowKeepsFocusVisible(t *testing.T) {
	var padded, sticky, bottom, opaque bool
	for _, r := range parseCSS(t, readStatic(t, "app.css")) {
		for _, sel := range strings.Split(r.selector, ",") {
			sel = strings.TrimSpace(sel)
			if strings.Contains(sel, ":has(.form-actions)") &&
				slices.ContainsFunc(r.decls, func(d string) bool { return strings.HasPrefix(d, "scroll-padding-bottom:") }) {
				padded = true
			}
			if sel != ".form-actions" {
				continue
			}
			sticky = sticky || slices.Contains(r.decls, "position: sticky")
			bottom = bottom || slices.Contains(r.decls, "bottom: 0")
			opaque = opaque || slices.ContainsFunc(r.decls, func(d string) bool { return strings.HasPrefix(d, "background:") })
		}
	}
	if !padded {
		t.Error("no rule for :has(.form-actions) declares scroll-padding-bottom")
	}
	if !sticky || !bottom || !opaque {
		t.Errorf(".form-actions: position sticky %v, bottom 0 %v, background %v; want all", sticky, bottom, opaque)
	}
}

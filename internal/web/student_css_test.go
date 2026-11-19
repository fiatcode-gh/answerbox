package web

import (
	"strings"
	"testing"
)

// selectorItems splits a rule's selector list into its trimmed items.
func selectorItems(r cssRule) []string {
	items := strings.Split(r.selector, ",")
	for i := range items {
		items[i] = strings.TrimSpace(items[i])
	}
	return items
}

// declValues returns the values the rule declares for prop, in order.
func declValues(r cssRule, prop string) []string {
	var vals []string
	for _, d := range r.decls {
		p, v, ok := strings.Cut(d, ":")
		if ok && strings.TrimSpace(p) == prop {
			vals = append(vals, strings.TrimSpace(v))
		}
	}
	return vals
}

// declProps returns the property names the rule declares.
func declProps(r cssRule) []string {
	var props []string
	for _, d := range r.decls {
		if p, _, ok := strings.Cut(d, ":"); ok {
			props = append(props, strings.TrimSpace(p))
		}
	}
	return props
}

// valuesFor collects the values of prop over every rule that has an item
// matching match.
func valuesFor(rules []cssRule, match func(item string) bool, prop string) []string {
	var vals []string
	for _, r := range rules {
		for _, item := range selectorItems(r) {
			if match(item) {
				vals = append(vals, declValues(r, prop)...)
				break
			}
		}
	}
	return vals
}

func exactly(sel string) func(string) bool {
	return func(item string) bool { return item == sel }
}

// cssSection returns the text of app.css section n, from its "/* n. " heading
// up to the next section's heading.
func cssSection(t *testing.T, n, next string) string {
	t.Helper()
	css := readStatic(t, "app.css")
	start := strings.Index(css, "/* "+n+". ")
	end := strings.Index(css, "/* "+next+". ")
	if start < 0 || end < start {
		t.Fatalf("app.css: section %s not found", n)
	}
	return css[start:end]
}

// The picked option's essentials (filled bubble, bold text, the check) must
// work on browsers without :has(); :has() only adds the row tint.
func TestCSS_PickedWorksWithoutHas(t *testing.T) {
	rules := parseCSS(t, readStatic(t, "app.css"))
	for _, want := range []struct {
		sel   string
		props []string
	}{
		{".option input:checked ~ .bubble", []string{"background", "color"}},
		{".option input:checked ~ .option-body", []string{"font-weight"}},
		{".option input:checked ~ .option-body::after", []string{"mask"}},
		{".option input:focus-visible ~ .bubble", []string{"outline"}},
	} {
		for _, prop := range want.props {
			if len(valuesFor(rules, exactly(want.sel), prop)) == 0 {
				t.Errorf("no rule for %q sets %s", want.sel, prop)
			}
		}
	}

	allowed := map[string]bool{"background": true, "background-color": true, "border-color": true, "box-shadow": true}
	tint := 0
	for _, r := range rules {
		if strings.Contains(r.selector, ":has(input:checked) .bubble") {
			t.Errorf("selector %q draws the picked bubble through :has()", r.selector)
		}
		if !strings.Contains(r.selector, ".option:has(input:checked)") {
			continue
		}
		tint++
		for _, p := range declProps(r) {
			if !allowed[p] {
				t.Errorf("rule %q sets %s through :has(); only the row tint may depend on it", r.selector, p)
			}
		}
	}
	if tint == 0 {
		t.Error("no .option:has(input:checked) rule tints the picked row")
	}
}

// Guards the student targets against a later shrink.
func TestCSS_StudentTargets(t *testing.T) {
	rules := parseCSS(t, readStatic(t, "app.css"))
	for _, want := range []struct {
		name  string
		match func(string) bool
		prop  string
		value string
	}{
		{".option", exactly(".option"), "min-height", "64px"},
		{".option .bubble", exactly(".option .bubble"), "width", "44px"},
		{".option .bubble", exactly(".option .bubble"), "height", "44px"},
		{"#submit-open", exactly("#submit-open"), "min-height", "56px"},
		{".answer-text", func(item string) bool { return strings.HasSuffix(item, ".answer-text") }, "min-height", "56px"},
	} {
		vals := valuesFor(rules, want.match, want.prop)
		if len(vals) == 0 || vals[len(vals)-1] != want.value {
			t.Errorf("%s: %s = %v, want %s", want.name, want.prop, vals, want.value)
		}
	}
}

// The exam bar's height is one token: section 11 positions nothing against a
// literal bar height.
func TestCSS_BarHeightToken(t *testing.T) {
	rules := parseCSS(t, cssSection(t, "11", "12"))
	for _, r := range rules {
		for _, prop := range []string{"top", "height", "max-height", "scroll-margin-top"} {
			for _, v := range declValues(r, prop) {
				if strings.Contains(v, "56px") || strings.Contains(v, "64px") {
					t.Errorf("rule %q: %s: %s uses a literal bar height", r.selector, prop, v)
				}
			}
		}
	}
	for _, want := range []struct {
		match func(string) bool
		prop  string
	}{
		{exactly(".exam-bar"), "height"},
		{exactly(".question"), "scroll-margin-top"},
		{func(item string) bool { return strings.HasSuffix(item, "#question-nav") }, "top"},
		{func(item string) bool { return strings.HasPrefix(item, "html.js #question-nav") }, "top"},
		{func(item string) bool { return strings.HasPrefix(item, "html.js #question-nav") }, "max-height"},
	} {
		vals := valuesFor(rules, want.match, want.prop)
		if len(vals) == 0 {
			t.Errorf("section 11: no %s for the bar", want.prop)
		}
		for _, v := range vals {
			if !strings.Contains(v, "var(--ge-bar)") {
				t.Errorf("section 11: %s: %s does not use var(--ge-bar)", want.prop, v)
			}
		}
	}
}

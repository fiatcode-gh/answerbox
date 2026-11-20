package web

import (
	"io/fs"
	"strings"
	"testing"
)

// TestTemplates_TitlesNameTheApp checks that every page title and the
// default title carry the app's name.
func TestTemplates_TitlesNameTheApp(t *testing.T) {
	const minTitles = 19 // templates defining a title when the app was renamed

	paths, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		t.Fatal(err)
	}
	titles := 0
	for _, p := range paths {
		b, err := fs.ReadFile(templateFS, p)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, `{{define "title"}}`) {
				continue
			}
			titles++
			if !strings.Contains(line, "Answerbox") {
				t.Errorf("%s:%d: title does not name the app: %s", p, i+1, line)
			}
		}
	}
	if titles < minTitles {
		t.Errorf("saw %d title lines, want at least %d", titles, minTitles)
	}

	base, err := fs.ReadFile(templateFS, "templates/base.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(base), `<title>{{block "title" .Page}}Answerbox{{end}}</title>`) {
		t.Error("base.html: default title does not name the app")
	}
	t.Logf("checked %d title lines", titles)
}

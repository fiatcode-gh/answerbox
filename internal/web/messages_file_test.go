package web

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

func TestMessages_FileCodes(t *testing.T) {
	english := examfile.English()
	want := []string{"file.line"}
	for code := range english {
		want = append(want, "file."+string(code))
	}
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(fileMessagesID)); !slices.Equal(got, want) {
		t.Errorf("keys differ from examfile.English():\n got %v\nwant %v", got, want)
	}
	check := func(key, id, format string) {
		t.Helper()
		if id == "" {
			t.Errorf("%q: empty text", key)
		}
		if strings.Contains(id, "{line}") {
			t.Errorf("%q: holds {line}", key)
		}
		ev, _ := verbs(format)
		iv, problems := verbs(id)
		for _, p := range problems {
			t.Errorf("%q: %s", key, p)
		}
		if !slices.Equal(ev, iv) {
			t.Errorf("%q: verbs differ: en %v, id %v", key, ev, iv)
		}
	}
	for code, format := range english {
		key := "file." + string(code)
		if id, ok := fileMessagesID[key]; ok {
			check(key, id, strings.Replace(format, "{line}", "", 1))
		}
	}
	check("file.line", fileMessagesID["file.line"], "%[1]d%[2]s")
}

// sampleArgs gives a format's verbs distinct values: numbers from 101, and
// image or file names for %s.
func sampleArgs(format string) []any {
	var args []any
	for i, v := range verbRe.FindAllStringSubmatch(format, -1) {
		switch v[5] {
		case "d":
			args = append(args, 101+i)
		case "s":
			args = append(args, "pic-"+strconv.Itoa(i)+".png")
		}
	}
	return args
}

func TestFileError_Table(t *testing.T) {
	en, id := localizer{langEN}, localizer{langID}
	for code, format := range examfile.English() {
		lines := []int{0}
		if strings.Contains(format, "{line}") {
			lines = append(lines, 37)
		}
		for _, line := range lines {
			args := sampleArgs(format)
			e := examfile.NewError(code, line, args...)
			name := fmt.Sprintf("%s line %d", code, line)
			if got := en.FileError(e); got != e.Msg {
				t.Errorf("%s: English %q, want %q", name, got, e.Msg)
			}
			got := id.FileError(e)
			if got == e.Msg {
				t.Errorf("%s: Indonesian is the English %q", name, got)
			}
			if prefixed := strings.HasPrefix(got, "baris 37: "); prefixed != (line > 0) {
				t.Errorf("%s: %q: line prefix %v, want %v", name, got, prefixed, line > 0)
			}
			for _, a := range args {
				if !strings.Contains(got, fmt.Sprint(a)) {
					t.Errorf("%s: %q lacks argument %v", name, got, a)
				}
			}
			if strings.Contains(got, "%!") {
				t.Errorf("%s: %q holds a fmt error", name, got)
			}
		}
	}
	for _, e := range []*examfile.Error{
		{Msg: "no code here"},
		{Msg: "line 3: unknown rule", Code: "no_such_rule", Line: 3, Args: []any{1}},
	} {
		for _, l := range []localizer{en, id} {
			if got := l.FileError(e); got != e.Msg {
				t.Errorf("%s, code %q: %q, want %q", l.lang, e.Code, got, e.Msg)
			}
		}
	}
}

func TestFileError_KeepsUserText(t *testing.T) {
	id := localizer{langID}
	for _, e := range []*examfile.Error{
		examfile.NewError(examfile.CodeHeaderDuplicate, 4, "<b>%d"),
		examfile.NewError(examfile.CodeImageMissingQuestion, 0, 2, "<b>%d.png"),
	} {
		got := id.FileError(e)
		if !strings.Contains(got, `"<b>%d`) || strings.Contains(got, "%!") {
			t.Errorf("%s: %q does not keep the user's text verbatim", e.Code, got)
		}
	}
}

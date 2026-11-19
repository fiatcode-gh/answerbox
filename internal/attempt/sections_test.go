package attempt_test

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/fiatcode-gh/answerbox/internal/attempt"
	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

// sectionText builds an exam with one section per size, "## Part K" with the
// instructions "Instructions K.", holding that many questions of three
// options each; option 1 of every question is the correct one.
func sectionText(shuffle string, show examfile.ShowResult, sizes ...int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Sectioned %s %s\nshuffle: %s\nshow_result: %s\n", shuffle, show, shuffle, show)
	n := 0
	for k, size := range sizes {
		fmt.Fprintf(&b, "\n## Part %d\nInstructions %d.\n", k+1, k+1)
		for range size {
			n++
			fmt.Fprintf(&b, "\n%d. Question %d\n- Option %d.1 *\n- Option %d.2\n- Option %d.3\n", n, n, n, n, n)
		}
	}
	return b.String()
}

// wantSections checks that the section of each shown question (1-based) is
// the file question's section at the same index of heads, and nil elsewhere.
func wantSections(t *testing.T, what string, got []*exam.Section, heads map[int]*exam.Section) {
	t.Helper()
	for i, s := range got {
		want := heads[i+1]
		if !reflect.DeepEqual(s, want) {
			t.Fatalf("%s %d: section = %+v, want %+v", what, i+1, s, want)
		}
	}
}

// fileHeads maps the 1-based positions given to the sections stored there.
func fileHeads(t *testing.T, qs []exam.Question, positions ...int) map[int]*exam.Section {
	t.Helper()
	heads := map[int]*exam.Section{}
	for k, p := range positions {
		s := qs[p-1].Section
		if s == nil || s.Title != fmt.Sprintf("Part %d", k+1) ||
			!strings.Contains(string(s.BodyHTML), fmt.Sprintf("Instructions %d.", k+1)) {
			t.Fatalf("file question %d: section = %+v, want Part %d with its instructions", p, s, k+1)
		}
		heads[p] = s
	}
	return heads
}

func viewSections(v attempt.View) []*exam.Section {
	out := make([]*exam.Section, len(v.Questions))
	for i, q := range v.Questions {
		out[i] = q.Section
	}
	return out
}

func TestView_SectionsStayTogether(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, sectionText("questions, options", examfile.ShowScore, 3, 4, 2))
	qs := h.questions(t, id)
	heads := fileHeads(t, qs, 1, 4, 8)
	runs := [][2]int{{1, 3}, {4, 7}, {8, 9}}

	var orders [][]int64
	for i := range 20 {
		token := h.start(t, fmt.Sprintf("Student %d", i))
		v := h.view(t, token)
		var ids []int64
		for j, q := range v.Questions {
			if q.Number != j+1 {
				t.Fatalf("student %d: question %d has number %d", i, j+1, q.Number)
			}
			ids = append(ids, q.ID)
		}
		wantSections(t, "shown question", viewSections(v), heads)
		for _, r := range runs {
			var shown, file []int64
			for n := r[0]; n <= r[1]; n++ {
				shown = append(shown, ids[n-1])
				file = append(file, qs[n-1].ID)
			}
			slices.Sort(shown)
			if !reflect.DeepEqual(shown, file) {
				t.Fatalf("student %d: questions shown at %d–%d = %v, want file questions %v", i, r[0], r[1], shown, file)
			}
		}
		if again := h.view(t, token); !reflect.DeepEqual(order(again), order(v)) {
			t.Fatalf("student %d: order changed on reload", i)
		}
		orders = append(orders, ids)
	}
	differ := false
	for _, o := range orders[1:] {
		if !reflect.DeepEqual(o, orders[0]) {
			differ = true
		}
	}
	if !differ {
		t.Fatal("all 20 students got the same order")
	}

	none := h.importExam(t, sectionText("none", examfile.ShowScore, 3, 4, 2))
	if err := h.exams.Open(t.Context(), h.owner, none); err != nil {
		t.Fatal(err)
	}
	nqs := h.questions(t, none)
	v := h.view(t, h.startIn(t, none, "Plain"))
	for i, q := range v.Questions {
		if q.ID != nqs[i].ID {
			t.Fatalf("shuffle none: question %d is %d, want file question %d", i+1, q.ID, nqs[i].ID)
		}
	}
	wantSections(t, "shuffle none question", viewSections(v), fileHeads(t, nqs, 1, 4, 8))
}

func TestView_ReviewSections(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, sectionText("questions, options", examfile.ShowReview, 3, 4, 2))
	qs := h.questions(t, id)
	token := h.start(t, "Ayu")
	before := viewSections(h.view(t, token))
	h.save(t, token, qs[0].ID, correctOption(qs[0]))
	h.save(t, token, qs[5].ID, correctOption(qs[5]))
	h.save(t, token, qs[8].ID, wrongOption(qs[8]))
	h.submit(t, token)

	r := h.view(t, token).Result
	if r == nil || r.Score != 2 || r.Total != 9 || len(r.Review) != 9 {
		t.Fatalf("result = %+v, want 2 / 9 with nine review questions", r)
	}
	after := make([]*exam.Section, len(r.Review))
	for i, q := range r.Review {
		after[i] = q.Section
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("review sections = %v, want those of the view %v", after, before)
	}
	wantSections(t, "review question", after, fileHeads(t, qs, 1, 4, 8))
}

func TestDetail_Sections(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, sectionText("questions", examfile.ShowScore, 3, 4, 2))
	qs := h.questions(t, id)
	heads := fileHeads(t, qs, 1, 4, 8)
	token := h.start(t, "Ayu")
	v := h.view(t, token)
	fifth := v.Questions[4].ID
	h.record(t, token, attempt.LeftPage, fifth)

	d := h.detail(t, id, h.only(t, id).ID)
	got := make([]*exam.Section, len(d.Questions))
	for i, q := range d.Questions {
		if q.Position != i+1 {
			t.Fatalf("detail question %d has position %d", i+1, q.Position)
		}
		got[i] = q.Section
	}
	wantSections(t, "detail question", got, heads)
	var position int
	for _, q := range qs {
		if q.ID == fifth {
			position = q.Position
		}
	}
	if len(d.Events) != 1 || d.Events[0].Screen != 5 || d.Events[0].Question != position {
		t.Fatalf("events = %+v, want one on screen 5 for file question %d", d.Events, position)
	}
}

func TestList_SectionScores(t *testing.T) {
	h := newHarness(t)
	id := h.openExam(t, sectionText("none", examfile.ShowScore, 2, 3))
	qs := h.questions(t, id)
	ayu := h.start(t, "Ayu")
	h.save(t, ayu, qs[0].ID, correctOption(qs[0]))
	h.save(t, ayu, qs[1].ID, wrongOption(qs[1]))
	h.save(t, ayu, qs[2].ID, correctOption(qs[2]))
	h.save(t, ayu, qs[3].ID, correctOption(qs[3]))
	h.submit(t, ayu)
	budi := h.start(t, "Budi")
	h.save(t, budi, qs[4].ID, correctOption(qs[4]))
	h.start(t, "Citra")

	res, err := h.svc.List(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if want := []attempt.ResultSection{{Title: "Part 1", Total: 2}, {Title: "Part 2", Total: 3}}; !reflect.DeepEqual(res.Sections, want) {
		t.Fatalf("sections = %+v, want %+v", res.Sections, want)
	}
	want := map[string][]int{"Ayu": {1, 2}, "Budi": {0, 1}, "Citra": {0, 0}}
	if len(res.Attempts) != 3 {
		t.Fatalf("attempts = %+v, want 3", res.Attempts)
	}
	for _, a := range res.Attempts {
		if !reflect.DeepEqual(a.SectionScores, want[a.StudentName]) {
			t.Fatalf("%s: section scores = %v, want %v", a.StudentName, a.SectionScores, want[a.StudentName])
		}
		sum := 0
		for _, s := range a.SectionScores {
			sum += s
		}
		if sum != a.Score {
			t.Fatalf("%s: section scores %v add up to %d, score is %d", a.StudentName, a.SectionScores, sum, a.Score)
		}
	}

	plain := h.importExam(t, examText("none", examfile.ShowScore, 3, 2))
	if err := h.exams.Open(t.Context(), h.owner, plain); err != nil {
		t.Fatal(err)
	}
	h.startIn(t, plain, "Dewi")
	res, err = h.svc.List(t.Context(), plain)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sections != nil || len(res.Attempts) != 1 || res.Attempts[0].SectionScores != nil {
		t.Fatalf("exam without sections: %+v, want no sections and nil section scores", res)
	}

	res, err = h.svc.List(t.Context(), 9999)
	if err != nil {
		t.Fatal(err)
	}
	if res.Attempts == nil || len(res.Attempts) != 0 || res.Sections != nil {
		t.Fatalf("unknown exam: %#v, want empty attempts", res)
	}
}

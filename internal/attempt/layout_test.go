package attempt

import (
	"reflect"
	"slices"
	"testing"

	"github.com/fiatcode-gh/go-exam/internal/exam"
	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// gridQuestions builds n questions of m options each in original order;
// IDs encode the positions so the order is readable in failures.
func gridQuestions(n, m int) []exam.Question {
	qs := make([]exam.Question, n)
	for i := range qs {
		qs[i] = exam.Question{ID: int64(100 * (i + 1)), Position: i + 1}
		for j := range m {
			qs[i].Options = append(qs[i].Options, exam.Option{ID: int64(100*(i+1) + j + 1), Position: j + 1})
		}
	}
	return qs
}

func questionPositions(qs []exam.Question) []int {
	out := make([]int, len(qs))
	for i, q := range qs {
		out[i] = q.Position
	}
	return out
}

func optionPositions(q exam.Question) []int {
	out := make([]int, len(q.Options))
	for i, o := range q.Options {
		out[i] = o.Position
	}
	return out
}

// byOriginal returns the laid-out questions indexed by original position.
func byOriginal(qs []exam.Question) map[int]exam.Question {
	m := map[int]exam.Question{}
	for _, q := range qs {
		m[q.Position] = q
	}
	return m
}

func TestLayout_Golden(t *testing.T) {
	got := layout(gridQuestions(5, 4), examfile.ShuffleQuestionsOptions, 42)

	if want := []int{2, 3, 5, 1, 4}; !reflect.DeepEqual(questionPositions(got), want) {
		t.Fatalf("question positions = %v, want %v", questionPositions(got), want)
	}
	want := [][]int{{1, 3, 4, 2}, {2, 4, 3, 1}, {2, 1, 4, 3}, {2, 4, 3, 1}, {3, 1, 2, 4}}
	orig := byOriginal(got)
	var opts [][]int
	for p := 1; p <= 5; p++ {
		opts = append(opts, optionPositions(orig[p]))
	}
	if !reflect.DeepEqual(opts, want) {
		t.Fatalf("option positions = %v, want %v", opts, want)
	}
	t.Logf("golden seed 42: questions %v options %v", questionPositions(got), opts)
}

func TestScreenNumbers(t *testing.T) {
	qs := gridQuestions(5, 4)
	shuffled := map[int]int{2: 1, 3: 2, 5: 3, 1: 4, 4: 5}
	for _, tc := range []struct {
		mode examfile.Shuffle
		want map[int]int
	}{
		{examfile.ShuffleQuestionsOptions, shuffled},
		{examfile.ShuffleQuestions, shuffled},
		{examfile.ShuffleNone, map[int]int{1: 1, 2: 2, 3: 3, 4: 4, 5: 5}},
	} {
		if got := screenNumbers(qs, tc.mode, 42); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("screenNumbers(%s) = %v, want %v", tc.mode, got, tc.want)
		}
	}
}

func TestLayout_Modes(t *testing.T) {
	const seed = 7
	identity := func(n int) []int {
		out := make([]int, n)
		for i := range out {
			out[i] = i + 1
		}
		return out
	}
	isPermutation := func(got []int, n int) bool {
		s := slices.Clone(got)
		slices.Sort(s)
		return reflect.DeepEqual(s, identity(n))
	}

	t.Run("none", func(t *testing.T) {
		got := layout(gridQuestions(6, 5), examfile.ShuffleNone, seed)
		if !reflect.DeepEqual(questionPositions(got), identity(6)) {
			t.Fatalf("questions = %v, want file order", questionPositions(got))
		}
		for _, q := range got {
			if !reflect.DeepEqual(optionPositions(q), identity(5)) {
				t.Fatalf("options of %d = %v, want file order", q.Position, optionPositions(q))
			}
		}
	})

	t.Run("questions", func(t *testing.T) {
		got := layout(gridQuestions(6, 5), examfile.ShuffleQuestions, seed)
		qp := questionPositions(got)
		if !isPermutation(qp, 6) || reflect.DeepEqual(qp, identity(6)) {
			t.Fatalf("questions = %v, want a non-identity permutation", qp)
		}
		for _, q := range got {
			if !reflect.DeepEqual(optionPositions(q), identity(5)) {
				t.Fatalf("options of %d = %v, want file order", q.Position, optionPositions(q))
			}
		}
	})

	t.Run("questions_options", func(t *testing.T) {
		got := layout(gridQuestions(6, 5), examfile.ShuffleQuestionsOptions, seed)
		qp := questionPositions(got)
		if !isPermutation(qp, 6) || reflect.DeepEqual(qp, identity(6)) {
			t.Fatalf("questions = %v, want a non-identity permutation", qp)
		}
		moved := false
		for _, q := range got {
			op := optionPositions(q)
			if !isPermutation(op, 5) {
				t.Fatalf("options of %d = %v, not a permutation", q.Position, op)
			}
			if !reflect.DeepEqual(op, identity(5)) {
				moved = true
			}
		}
		if !moved {
			t.Fatal("no option list was shuffled")
		}
	})

	t.Run("input untouched", func(t *testing.T) {
		in := gridQuestions(4, 3)
		layout(in, examfile.ShuffleQuestionsOptions, seed)
		if !reflect.DeepEqual(in, gridQuestions(4, 3)) {
			t.Fatal("layout mutated its input")
		}
	})
}

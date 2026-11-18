package attempt

import (
	"fmt"
	"math"
	"math/rand/v2"
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

// layoutBeforeSections is the body of layout as released in 293ab19, before
// sections existed. TestLayout_UnsectionedOrderUnchanged holds the current
// layout to it for every exam without sections.
func layoutBeforeSections(questions []exam.Question, mode examfile.Shuffle, seed int64) []exam.Question {
	src := rand.NewPCG(uint64(seed), layoutStream)
	shuffle := func(n int, swap func(i, j int)) {
		for i := n - 1; i > 0; i-- {
			swap(i, int(src.Uint64()%uint64(i+1)))
		}
	}

	original := make([]exam.Question, len(questions))
	for i, q := range questions {
		q.Options = slices.Clone(q.Options)
		original[i] = q
	}
	out := slices.Clone(original)
	if mode != examfile.ShuffleNone {
		shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	}
	if mode == examfile.ShuffleQuestionsOptions {
		for _, q := range original {
			opts := q.Options
			shuffle(len(opts), func(i, j int) { opts[i], opts[j] = opts[j], opts[i] })
		}
	}
	return out
}

func TestLayout_UnsectionedOrderUnchanged(t *testing.T) {
	seeds := []int64{0, 1, 42, -1, math.MinInt64, math.MaxInt64}
	gen := rand.New(rand.NewPCG(2026, 11))
	for range 200 {
		seeds = append(seeds, gen.Int64()-gen.Int64())
	}
	modes := []examfile.Shuffle{examfile.ShuffleNone, examfile.ShuffleQuestions, examfile.ShuffleQuestionsOptions}
	for _, mode := range modes {
		for _, n := range []int{0, 1, 2, 5, 20, 40} {
			for _, m := range []int{2, 4, 5} {
				for _, seed := range seeds {
					want := layoutBeforeSections(gridQuestions(n, m), mode, seed)
					if got := layout(gridQuestions(n, m), mode, seed); !reflect.DeepEqual(got, want) {
						t.Fatalf("layout(%d×%d, %s, %d) differs from the 293ab19 layout", n, m, mode, seed)
					}
				}
			}
		}
	}
}

// sectionedQuestions is gridQuestions(n, 3) with a section starting at each
// of starts (1-based positions), titled "Part K".
func sectionedQuestions(n int, starts ...int) []exam.Question {
	qs := gridQuestions(n, 3)
	for k, p := range starts {
		qs[p-1].Section = &exam.Section{Title: fmt.Sprintf("Part %d", k+1)}
	}
	return qs
}

func TestLayout_Sections(t *testing.T) {
	runs := [][2]int{{0, 3}, {3, 7}, {7, 9}} // index ranges of the three sections
	sorted := func(qs []exam.Question) []int {
		p := questionPositions(qs)
		slices.Sort(p)
		return p
	}
	checkHeads := func(t *testing.T, got []exam.Question) {
		t.Helper()
		heads := map[int]string{0: "Part 1", 3: "Part 2", 7: "Part 3"}
		for i, q := range got {
			title, want := heads[i]
			if (q.Section != nil) != want || want && q.Section.Title != title {
				t.Fatalf("index %d: section %+v, want %q (%v)", i, q.Section, title, want)
			}
		}
	}

	for _, mode := range []examfile.Shuffle{examfile.ShuffleQuestions, examfile.ShuffleQuestionsOptions} {
		reordered := false
		for seed := range int64(50) {
			in := sectionedQuestions(9, 1, 4, 8)
			got := layout(in, mode, seed)
			for _, r := range runs {
				run := got[r[0]:r[1]]
				var want []int
				for p := r[0] + 1; p <= r[1]; p++ {
					want = append(want, p)
				}
				if !reflect.DeepEqual(sorted(run), want) {
					t.Fatalf("%s seed %d: indexes %d–%d hold %v, want a permutation of %v",
						mode, seed, r[0], r[1]-1, questionPositions(run), want)
				}
				if !reflect.DeepEqual(questionPositions(run), want) {
					reordered = true
				}
			}
			checkHeads(t, got)
			if !reflect.DeepEqual(in, sectionedQuestions(9, 1, 4, 8)) {
				t.Fatalf("%s seed %d: layout mutated its input", mode, seed)
			}
		}
		if !reordered {
			t.Fatalf("%s: no seed reordered a section", mode)
		}
	}

	got := layout(sectionedQuestions(9, 1, 4, 8), examfile.ShuffleNone, 7)
	if want := []int{1, 2, 3, 4, 5, 6, 7, 8, 9}; !reflect.DeepEqual(questionPositions(got), want) {
		t.Fatalf("none: questions = %v, want file order", questionPositions(got))
	}
	checkHeads(t, got)

	// Questions before a first section (not produced by the app) form a run
	// of their own.
	for seed := range int64(50) {
		got := layout(sectionedQuestions(6, 3), examfile.ShuffleQuestions, seed)
		if p := sorted(got[:2]); !reflect.DeepEqual(p, []int{1, 2}) || got[2].Section == nil {
			t.Fatalf("seed %d: questions = %v, want 1 and 2 first and the section heading at index 2", seed, questionPositions(got))
		}
	}
}

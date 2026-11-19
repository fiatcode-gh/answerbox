package attempt

import (
	"math/rand/v2"
	"slices"

	"github.com/fiatcode-gh/answerbox/internal/exam"
	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

// layoutStream is the fixed PCG stream for every attempt; changing it
// changes every attempt's order (pinned by TestLayout_Golden).
const layoutStream = 0x657a6578616d

// layout returns questions (given in Position order) in the attempt's display
// order. It derives the order from seed and mode alone and never mutates its
// input.
func layout(questions []exam.Question, mode examfile.Shuffle, seed int64) []exam.Question {
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
		// One run per section, in file order. An exam without sections is a
		// single run, which draws exactly what the shuffle drew before
		// sections existed (TestLayout_Golden).
		for start := 0; start < len(out); {
			end := start + 1
			for end < len(out) && out[end].Section == nil {
				end++
			}
			run := out[start:end]
			head := run[0].Section
			run[0].Section = nil
			shuffle(len(run), func(i, j int) { run[i], run[j] = run[j], run[i] })
			run[0].Section = head // the heading stays above the run's first shown question
			start = end
		}
	}
	if mode == examfile.ShuffleQuestionsOptions {
		// original and out share each question's Options backing array, so
		// shuffling in original order reorders the displayed options too.
		for _, q := range original {
			opts := q.Options
			shuffle(len(opts), func(i, j int) { opts[i], opts[j] = opts[j], opts[i] })
		}
	}
	return out
}

// screenNumbers maps each question's file position to its 1-based number on
// the student's screen for the attempt with this shuffle mode and seed.
func screenNumbers(questions []exam.Question, mode examfile.Shuffle, seed int64) map[int]int {
	out := make(map[int]int, len(questions))
	for i, q := range layout(questions, mode, seed) {
		out[q.Position] = i + 1
	}
	return out
}

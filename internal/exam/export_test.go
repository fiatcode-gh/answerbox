package exam

import (
	"context"

	"github.com/fiatcode-gh/answerbox/internal/examfile"
)

// ReplaceContent exposes replaceContent so a test can reach the stale check
// inside the transaction.
func (s *Service) ReplaceContent(ctx context.Context, actor Actor, examID int64, base int, summary string, ex examfile.Exam, key string) error {
	return s.replaceContent(ctx, actor, examID, base, summary, ex, key)
}

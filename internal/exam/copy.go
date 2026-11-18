package exam

import (
	"context"
	"errors"
	"fmt"

	"github.com/fiatcode-gh/go-exam/internal/examfile"
)

// Copy makes a new exam from the current revision of an exam in actor's
// scope: closed, without a schedule, with a new code and its own images,
// owned by actor (also when actor is an admin copying another teacher's
// exam). Attempts are not copied. It returns the new exam's id. An exam
// outside the scope is ErrNotFound; one without a revision is ErrNoRevision.
func (s *Service) Copy(ctx context.Context, actor Actor, examID int64) (int64, error) {
	cur, upload, err := s.currentUpload(ctx, actor, examID)
	if err != nil {
		return 0, err
	}
	ex, key, err := s.store(upload)
	if fileErr := (*examfile.Error)(nil); errors.As(err, &fileErr) {
		// %v, not %w: a stored revision that does not parse is a server
		// fault, never an invalid upload.
		return 0, fmt.Errorf("exam: copy of exam %d: stored revision does not parse: %v", examID, err)
	}
	if err != nil {
		return 0, err
	}
	id, err := s.insertExam(ctx, actor.TeacherID, copiedSummary(cur.Exam.Title, cur.Code, cur.Number), ex, key)
	if err != nil {
		s.media.RemoveAll(key)
		return 0, err
	}
	return id, nil
}

func copiedSummary(title, code string, number int) string {
	return fmt.Sprintf("Copied from %s (%s), revision %d", title, code, number)
}

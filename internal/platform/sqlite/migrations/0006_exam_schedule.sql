-- Exam length and schedule. Every exam has its own length in minutes
-- (existing exams keep 60) and may have an open time and a close time
-- (unix ms; NULL = none). While an exam has a schedule the two times alone
-- decide whether it is open, so the button state is_open stays 0.
ALTER TABLE exam ADD COLUMN duration_minutes INTEGER NOT NULL DEFAULT 60
  CHECK (duration_minutes BETWEEN 5 AND 240);
ALTER TABLE exam ADD COLUMN opens_at INTEGER;
ALTER TABLE exam ADD COLUMN closes_at INTEGER
  CHECK (opens_at IS NULL OR closes_at IS NULL OR opens_at < closes_at)
  CHECK (is_open = 0 OR (opens_at IS NULL AND closes_at IS NULL));

-- The length is fixed with the questions once an attempt exists.
CREATE TRIGGER exam_duration_frozen BEFORE UPDATE OF duration_minutes ON exam
WHEN NEW.duration_minutes IS NOT OLD.duration_minutes
  AND EXISTS (SELECT 1 FROM attempt WHERE exam_id = OLD.id)
BEGIN
  SELECT RAISE(ABORT, 'exam is frozen');
END;

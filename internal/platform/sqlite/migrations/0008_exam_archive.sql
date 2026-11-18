-- Exam archive. archived_at is when the exam was archived (unix ms; NULL =
-- not archived). An archived exam is closed and has no schedule; the check
-- makes an archived exam that is open or scheduled unstorable, also for a
-- binary from before this migration.
ALTER TABLE exam ADD COLUMN archived_at INTEGER
  CHECK (archived_at IS NULL OR (is_open = 0 AND opens_at IS NULL AND closes_at IS NULL));

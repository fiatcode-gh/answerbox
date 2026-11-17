-- Exam revisions. Every change to an exam's content is stored as a numbered
-- revision: who made it, when, a summary, the content settings and the
-- questions (a JSON document: source text and rendered HTML of every question
-- and option, the answer key, and the image files). Revisions are append-only
-- and stop once an attempt exists. Exams that existed before this migration
-- get revision 1 at the next start, which is why revision 1 may be added to
-- an exam that already has attempts.
CREATE TABLE exam_revision (
  exam_id          INTEGER NOT NULL REFERENCES exam(id),
  number           INTEGER NOT NULL CHECK (number >= 1),
  created_at       INTEGER NOT NULL,              -- unix ms
  teacher_id       INTEGER NOT NULL REFERENCES teacher(id),
  summary          TEXT    NOT NULL CHECK (summary <> ''),
  title            TEXT    NOT NULL,
  shuffle          TEXT    NOT NULL CHECK (shuffle IN ('none','questions','questions_options')),
  show_result      TEXT    NOT NULL CHECK (show_result IN ('none','score','review')),
  monitor          INTEGER NOT NULL CHECK (monitor IN (0,1)),
  duration_minutes INTEGER NOT NULL CHECK (duration_minutes BETWEEN 5 AND 240),
  media_key        TEXT    NOT NULL,
  content          TEXT    NOT NULL CHECK (json_valid(content)),
  PRIMARY KEY (exam_id, number)
) STRICT;

CREATE TRIGGER exam_revision_in_sequence BEFORE INSERT ON exam_revision
WHEN NEW.number IS NOT (SELECT COALESCE(MAX(number), 0) + 1 FROM exam_revision WHERE exam_id = NEW.exam_id)
BEGIN
  SELECT RAISE(ABORT, 'revision number is out of sequence');
END;

CREATE TRIGGER exam_revision_frozen BEFORE INSERT ON exam_revision
WHEN NEW.number > 1 AND EXISTS (SELECT 1 FROM attempt WHERE exam_id = NEW.exam_id)
BEGIN
  SELECT RAISE(ABORT, 'exam is frozen');
END;

CREATE TRIGGER exam_revision_no_update BEFORE UPDATE ON exam_revision
BEGIN
  SELECT RAISE(ABORT, 'revisions are append-only');
END;

CREATE TRIGGER exam_revision_no_delete BEFORE DELETE ON exam_revision
BEGIN
  SELECT RAISE(ABORT, 'revisions are append-only');
END;

-- An exam's content settings always equal its latest revision, so they
-- change only together with a new revision (and never once it is frozen).
CREATE TRIGGER exam_content_matches_revision
BEFORE UPDATE OF title, shuffle, show_result, monitor, duration_minutes, media_key ON exam
WHEN EXISTS (SELECT 1 FROM exam_revision WHERE exam_id = OLD.id)
 AND NOT EXISTS (
   SELECT 1 FROM exam_revision r
   WHERE r.exam_id = OLD.id
     AND r.number = (SELECT MAX(number) FROM exam_revision WHERE exam_id = OLD.id)
     AND r.title = NEW.title AND r.shuffle = NEW.shuffle AND r.show_result = NEW.show_result
     AND r.monitor = NEW.monitor AND r.duration_minutes = NEW.duration_minutes
     AND r.media_key = NEW.media_key)
BEGIN
  SELECT RAISE(ABORT, 'exam content must match its latest revision');
END;

-- The revision an attempt was started on: the exam's current one, set by the
-- schema at insert and fixed from then on. NULL only while the exam has no
-- revision yet; the startup backfill fills it together with revision 1.
ALTER TABLE attempt ADD COLUMN revision INTEGER;

CREATE TRIGGER attempt_revision_current BEFORE INSERT ON attempt
WHEN NEW.revision IS NOT NULL
 AND NEW.revision IS NOT (SELECT MAX(number) FROM exam_revision WHERE exam_id = NEW.exam_id)
BEGIN
  SELECT RAISE(ABORT, 'attempt revision must be the exam''s current revision');
END;

CREATE TRIGGER attempt_revision_assign AFTER INSERT ON attempt
WHEN NEW.revision IS NULL
BEGIN
  UPDATE attempt SET revision = (SELECT MAX(number) FROM exam_revision WHERE exam_id = NEW.exam_id)
  WHERE id = NEW.id;
END;

CREATE TRIGGER attempt_revision_fixed BEFORE UPDATE OF revision ON attempt
WHEN NEW.revision IS NOT OLD.revision
 AND (OLD.revision IS NOT NULL
      OR NEW.revision IS NOT (SELECT MAX(number) FROM exam_revision WHERE exam_id = OLD.exam_id))
BEGIN
  SELECT RAISE(ABORT, 'attempt revision is fixed');
END;

CREATE TABLE exam (
  id          INTEGER PRIMARY KEY,
  title       TEXT    NOT NULL,
  shuffle     TEXT    NOT NULL CHECK (shuffle IN ('none','questions','questions_options')),
  show_result TEXT    NOT NULL CHECK (show_result IN ('none','score','review')),
  is_open     INTEGER NOT NULL DEFAULT 0 CHECK (is_open IN (0,1)),
  media_key   TEXT    NOT NULL UNIQUE,
  created_at  INTEGER NOT NULL
) STRICT;
CREATE UNIQUE INDEX exam_one_open ON exam(is_open) WHERE is_open = 1;

CREATE TABLE question (
  id        INTEGER PRIMARY KEY,
  exam_id   INTEGER NOT NULL REFERENCES exam(id) ON DELETE CASCADE,
  position  INTEGER NOT NULL,              -- 1-based original file order
  body_html TEXT    NOT NULL,
  UNIQUE (exam_id, position)
) STRICT;

CREATE TABLE question_option (
  id          INTEGER PRIMARY KEY,
  question_id INTEGER NOT NULL REFERENCES question(id) ON DELETE CASCADE,
  position    INTEGER NOT NULL,            -- 1-based original file order
  body_html   TEXT    NOT NULL,
  is_correct  INTEGER NOT NULL CHECK (is_correct IN (0,1)),
  UNIQUE (question_id, position)
) STRICT;

CREATE TABLE attempt (
  id           INTEGER PRIMARY KEY,
  exam_id      INTEGER NOT NULL REFERENCES exam(id),
  student_name TEXT    NOT NULL,
  token_hash   BLOB    NOT NULL UNIQUE,
  resume_code  TEXT    UNIQUE,
  shuffle_seed INTEGER NOT NULL,
  status       TEXT    NOT NULL DEFAULT 'in_progress'
               CHECK (status IN ('in_progress','submitted','timed_out')),
  started_at   INTEGER NOT NULL,
  deadline_at  INTEGER NOT NULL,
  finished_at  INTEGER,
  CHECK ((status = 'in_progress') = (finished_at IS NULL)),
  CHECK (status = 'in_progress' OR resume_code IS NULL)
) STRICT;
CREATE INDEX attempt_exam ON attempt(exam_id, started_at);
CREATE INDEX attempt_due  ON attempt(deadline_at) WHERE status = 'in_progress';

CREATE TABLE answer (
  attempt_id  INTEGER NOT NULL REFERENCES attempt(id) ON DELETE CASCADE,
  question_id INTEGER NOT NULL REFERENCES question(id),
  option_id   INTEGER NOT NULL REFERENCES question_option(id),
  PRIMARY KEY (attempt_id, question_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE admin_session (
  token_hash BLOB    PRIMARY KEY,
  csrf_token TEXT    NOT NULL,
  expires_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- Freeze triggers: once an attempt exists for an exam, its questions and
-- answer key never change.
CREATE TRIGGER question_frozen_insert BEFORE INSERT ON question
WHEN EXISTS (SELECT 1 FROM attempt WHERE exam_id = NEW.exam_id)
BEGIN
  SELECT RAISE(ABORT, 'exam is frozen');
END;

CREATE TRIGGER question_frozen_update BEFORE UPDATE ON question
WHEN EXISTS (SELECT 1 FROM attempt WHERE exam_id = OLD.exam_id)
BEGIN
  SELECT RAISE(ABORT, 'exam is frozen');
END;

CREATE TRIGGER question_frozen_delete BEFORE DELETE ON question
WHEN EXISTS (SELECT 1 FROM attempt WHERE exam_id = OLD.exam_id)
BEGIN
  SELECT RAISE(ABORT, 'exam is frozen');
END;

CREATE TRIGGER question_option_frozen_insert BEFORE INSERT ON question_option
WHEN EXISTS (SELECT 1 FROM attempt a JOIN question q ON q.exam_id = a.exam_id WHERE q.id = NEW.question_id)
BEGIN
  SELECT RAISE(ABORT, 'exam is frozen');
END;

CREATE TRIGGER question_option_frozen_update BEFORE UPDATE ON question_option
WHEN EXISTS (SELECT 1 FROM attempt a JOIN question q ON q.exam_id = a.exam_id WHERE q.id = OLD.question_id)
BEGIN
  SELECT RAISE(ABORT, 'exam is frozen');
END;

CREATE TRIGGER question_option_frozen_delete BEFORE DELETE ON question_option
WHEN EXISTS (SELECT 1 FROM attempt a JOIN question q ON q.exam_id = a.exam_id WHERE q.id = OLD.question_id)
BEGIN
  SELECT RAISE(ABORT, 'exam is frozen');
END;

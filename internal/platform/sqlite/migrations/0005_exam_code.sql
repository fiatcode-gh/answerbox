-- Exam codes: every exam has a short code that students type to enter it,
-- and several exams may be open at once. Exams that existed before this
-- migration get their code at the next start; from here on an exam cannot be
-- stored without one, and a code that is set never changes.
DROP INDEX exam_one_open;

ALTER TABLE exam ADD COLUMN code TEXT
  CHECK (length(code) = 6 AND code NOT GLOB '*[^ABCDEFGHJKMNPQRSTVWXYZ23456789]*');
CREATE UNIQUE INDEX exam_code ON exam(code);

CREATE TRIGGER exam_code_required_insert BEFORE INSERT ON exam
WHEN NEW.code IS NULL
BEGIN
  SELECT RAISE(ABORT, 'exam needs a code');
END;

CREATE TRIGGER exam_code_fixed BEFORE UPDATE OF code ON exam
WHEN OLD.code IS NOT NULL AND NEW.code IS NOT OLD.code
BEGIN
  SELECT RAISE(ABORT, 'exam code is fixed');
END;

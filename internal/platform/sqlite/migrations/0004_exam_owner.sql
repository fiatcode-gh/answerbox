-- Exam ownership. Exams that existed before this migration get their owner at
-- the next start (the first ANSWERBOX_ADMIN_EMAILS entry); from here on an exam
-- cannot be stored or left without one.
ALTER TABLE exam ADD COLUMN owner_id INTEGER REFERENCES teacher(id);
CREATE INDEX exam_owner ON exam(owner_id);

CREATE TRIGGER exam_owner_required_insert BEFORE INSERT ON exam
WHEN NEW.owner_id IS NULL
BEGIN
  SELECT RAISE(ABORT, 'exam needs an owner');
END;

CREATE TRIGGER exam_owner_required_update BEFORE UPDATE OF owner_id ON exam
WHEN NEW.owner_id IS NULL
BEGIN
  SELECT RAISE(ABORT, 'exam needs an owner');
END;

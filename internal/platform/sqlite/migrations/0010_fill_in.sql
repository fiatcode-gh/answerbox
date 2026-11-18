-- Fill-in questions. Accepted answers are a property of the question row: a
-- JSON array of 1 to 10 strings, NULL for a multiple-choice question. They
-- are frozen by the question freeze triggers of 0001 and replaced with the
-- rows. A binary from before this migration writes questions without them.
-- typed_answer holds a student's typed answer and the teacher's latest
-- override of it, and goes with its attempt.
ALTER TABLE question ADD COLUMN accepted_answers TEXT
  CHECK (accepted_answers IS NULL OR
         CASE WHEN json_valid(accepted_answers)
              THEN json_type(accepted_answers) = 'array' AND json_array_length(accepted_answers) BETWEEN 1 AND 10
              ELSE 0 END);

CREATE TABLE typed_answer (
  attempt_id       INTEGER NOT NULL REFERENCES attempt(id) ON DELETE CASCADE,
  question_id      INTEGER NOT NULL REFERENCES question(id),
  text             TEXT    NOT NULL CHECK (text <> '' AND length(text) <= 200),
  override_correct INTEGER CHECK (override_correct IN (0,1)),
  override_by      INTEGER REFERENCES teacher(id),
  override_at      INTEGER,                      -- unix ms
  PRIMARY KEY (attempt_id, question_id),
  CHECK ((override_correct IS NULL) = (override_by IS NULL) AND (override_by IS NULL) = (override_at IS NULL))
) STRICT, WITHOUT ROWID;

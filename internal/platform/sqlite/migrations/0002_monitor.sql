-- Anti-cheat monitor: a per-exam switch (existing exams default to on) and
-- the events a student's browser reports during an attempt.
ALTER TABLE exam ADD COLUMN monitor INTEGER NOT NULL DEFAULT 1 CHECK (monitor IN (0,1));

CREATE TABLE attempt_event (
  id         INTEGER PRIMARY KEY,
  attempt_id INTEGER NOT NULL REFERENCES attempt(id) ON DELETE CASCADE,
  kind       TEXT    NOT NULL CHECK (kind IN ('left_page','copy','paste','right_click','resize')),
  question   INTEGER,                      -- 1-based original file position; NULL when unknown
  at         INTEGER NOT NULL              -- server time, unix ms
) STRICT;
CREATE INDEX attempt_event_attempt ON attempt_event(attempt_id, at);

-- Teacher accounts: who may sign in, and sessions that belong to a teacher.
-- A removed teacher keeps the row (allowed = 0) because exams refer to it.
CREATE TABLE teacher (
  id            INTEGER PRIMARY KEY,
  email         TEXT    NOT NULL UNIQUE CHECK (email <> '' AND email = lower(email)),
  name          TEXT    NOT NULL DEFAULT '',
  allowed       INTEGER NOT NULL DEFAULT 1 CHECK (allowed IN (0,1)),
  last_login_at INTEGER,                    -- unix ms; NULL until the first sign-in
  created_at    INTEGER NOT NULL
) STRICT;

-- Sessions had no owner and were cleared at every start, so none is worth keeping.
DROP TABLE admin_session;
CREATE TABLE admin_session (
  token_hash BLOB    PRIMARY KEY,
  teacher_id INTEGER NOT NULL REFERENCES teacher(id) ON DELETE CASCADE,
  csrf_token TEXT    NOT NULL,
  expires_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;
CREATE INDEX admin_session_teacher ON admin_session(teacher_id);

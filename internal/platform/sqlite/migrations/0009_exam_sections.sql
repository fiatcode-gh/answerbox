-- Exam sections. A question that starts a section carries the section's
-- title and rendered instructions ('' for none); every other question has
-- NULL in both. A section therefore always has a first question, is deleted
-- with it, and is frozen by the question freeze triggers of 0001. A binary
-- from before this migration writes questions without sections.
ALTER TABLE question ADD COLUMN section_title TEXT;
ALTER TABLE question ADD COLUMN section_html TEXT
  CHECK ((section_title IS NULL AND section_html IS NULL)
      OR (section_title IS NOT NULL AND section_title <> '' AND section_html IS NOT NULL));

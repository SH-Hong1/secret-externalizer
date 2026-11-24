-- The kinds of job each worker asks for, so that the web program can tell
-- that no worker serves one kind while workers of another kind are alive.
-- A worker recorded before this column existed ran every kind.
ALTER TABLE worker_beat ADD COLUMN kinds text[] NOT NULL DEFAULT '{scan,apply-check,apply}';

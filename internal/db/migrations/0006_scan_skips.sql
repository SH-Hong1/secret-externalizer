-- What a scan could not look at: the paths of the first skipped files and
-- the number of errors each scanner reported for files it analysed only in
-- part. A path is relative to the project folder, valid UTF-8, and free of
-- control characters; it is never file content.
ALTER TABLE scan ADD COLUMN skipped_files text[] NOT NULL DEFAULT '{}';
ALTER TABLE scan ADD COLUMN scanner_errors jsonb NOT NULL DEFAULT '{}';

-- The name shown for the user who asked for the scan. Scans requested before
-- this column existed keep an empty name and are shown by subject.
ALTER TABLE scan ADD COLUMN requester_name text NOT NULL DEFAULT '';

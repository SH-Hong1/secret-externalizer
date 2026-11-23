-- The configuration key a value is assigned to in its file, such as
-- live.db.password, or '' when the scan read none. It names a setting and
-- never holds a value. A finding is identified by its project, fingerprint,
-- file and key, so one value under different keys of a file is several
-- findings. Findings stored before this migration have no key.
ALTER TABLE finding ADD COLUMN key text NOT NULL DEFAULT ''
    CHECK (key ~ '^[A-Za-z0-9_.-]{0,200}$');
ALTER TABLE finding DROP CONSTRAINT finding_project_id_fingerprint_file_path_key;
-- Also serves lookups by project, since project_id leads.
ALTER TABLE finding ADD CONSTRAINT finding_identity UNIQUE (project_id, fingerprint, file_path, key);

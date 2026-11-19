ALTER TABLE scan ADD COLUMN dropped integer NOT NULL DEFAULT 0;

-- A value found in one file of a project, identified by its fingerprint. It
-- never holds the value: only the fingerprint, a masked preview, and whether
-- OpenBao keeps the original. Customer data never has an original.
CREATE TABLE finding (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id    bigint NOT NULL REFERENCES project (id),
    fingerprint   text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    file_path     text NOT NULL CHECK (file_path <> ''),
    category      text NOT NULL CHECK (category IN
                      ('partner-session', 'storage-credential', 'ssh-credential', 'customer-data', 'other-secret')),
    kind          text NOT NULL CHECK (kind IN
                      ('phone', 'rrn', 'email', 'card', 'address',
                       'password', 'token', 'account', 'host', 'url', 'private-key', 'key-file', 'other')),
    preview       text NOT NULL,
    has_original  boolean NOT NULL,
    first_scan_id bigint NOT NULL REFERENCES scan (id),
    last_scan_id  bigint NOT NULL REFERENCES scan (id),
    CHECK (category <> 'customer-data' OR NOT has_original),
    -- Also serves lookups by project, since project_id leads.
    UNIQUE (project_id, fingerprint, file_path)
);

-- One place a scan found a finding. Lines and columns are 1-based; columns
-- count characters, and the end is the last character of the value. context
-- holds masked surrounding lines and is empty for customer data.
CREATE TABLE location (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    finding_id         bigint NOT NULL REFERENCES finding (id),
    scan_id            bigint NOT NULL REFERENCES scan (id),
    start_line         integer NOT NULL CHECK (start_line >= 1),
    start_col          integer NOT NULL CHECK (start_col >= 1),
    end_line           integer NOT NULL CHECK (end_line >= start_line),
    end_col            integer NOT NULL CHECK (end_col >= 1),
    context            text[] NOT NULL DEFAULT '{}',
    context_first_line integer NOT NULL DEFAULT 0 CHECK (context_first_line >= 0),
    file_hash          text NOT NULL CHECK (file_hash ~ '^[0-9a-f]{64}$'),
    detections         jsonb NOT NULL CHECK (jsonb_typeof(detections) = 'array')
);

CREATE INDEX location_scan_id ON location (scan_id);

CREATE INDEX location_finding_id ON location (finding_id);

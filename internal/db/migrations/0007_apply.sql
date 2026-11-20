-- One request to apply the verdicts of a project to its source. A check
-- fills it (checking, then ready or blocked); a confirmed run is queued and
-- then runs. It never holds a value.
CREATE TABLE apply_run (
    id                 bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id         bigint NOT NULL REFERENCES project (id),
    scan_id            bigint NOT NULL REFERENCES scan (id),
    state              text NOT NULL DEFAULT 'checking' CHECK (state IN
                           ('checking', 'ready', 'blocked', 'queued', 'running', 'succeeded', 'failed', 'superseded')),
    plan_digest        text NOT NULL CHECK (plan_digest ~ '^[0-9a-f]{64}$'),
    requested_by       text NOT NULL,
    requester_name     text NOT NULL,
    requested_at       timestamptz NOT NULL DEFAULT now(),
    checked_at         timestamptz,
    confirmed_at       timestamptz,
    started_at         timestamptz,
    finished_at        timestamptz,
    allow_irreversible boolean NOT NULL DEFAULT false,
    -- The untracked files holding customer data that the check showed. An
    -- apply redacts customer data in an untracked file only if it is here.
    irreversible_files text[] NOT NULL DEFAULT '{}',
    blockers           text[] NOT NULL DEFAULT '{}',
    dirty_files        text[] NOT NULL DEFAULT '{}',
    dirty_count        integer NOT NULL DEFAULT 0,
    failure_reason     text NOT NULL DEFAULT '',
    applied            integer NOT NULL DEFAULT 0,
    skipped            integer NOT NULL DEFAULT 0,
    failed             integer NOT NULL DEFAULT 0,
    rescan_id          bigint REFERENCES scan (id)
);
CREATE UNIQUE INDEX apply_one_active_per_project ON apply_run (project_id)
    WHERE state IN ('checking', 'queued', 'running');

-- The outcome for one location. It refers to the run only, so the record
-- stays when a bundle or finding is gone. It never holds a value.
CREATE TABLE apply_item (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    apply_id      bigint NOT NULL REFERENCES apply_run (id),
    bundle_id     bigint NOT NULL,
    finding_id    bigint NOT NULL,
    file_path     text NOT NULL,
    start_line    integer NOT NULL,
    start_col     integer NOT NULL,
    line          integer NOT NULL DEFAULT 0,
    action        text NOT NULL CHECK (action IN ('replace', 'redact', 'remove-file')),
    category      text NOT NULL,
    kind          text NOT NULL,
    name          text NOT NULL DEFAULT '',
    store_path    text NOT NULL DEFAULT '',
    store_version integer NOT NULL DEFAULT 0,
    tracked       boolean NOT NULL DEFAULT false,
    code_change   boolean NOT NULL DEFAULT false,
    status        text NOT NULL CHECK (status IN ('planned', 'writing', 'applied', 'skipped', 'failed')),
    reason        text NOT NULL DEFAULT '' CHECK (reason IN
                      ('', 'file-changed', 'file-missing', 'not-text', 'value-mismatch', 'not-allowed', 'overlap',
                       'untracked-irreversible', 'nested-repository', 'key-in-source-file',
                       'key-holds-customer-data', 'file-removed', 'too-large', 'store-failed',
                       'write-failed', 'interrupted')),
    in_manifest   boolean NOT NULL DEFAULT false,
    UNIQUE (apply_id, finding_id, start_line, start_col)
);

-- One file an apply changes, written down before the change. before_hash is
-- empty for a file the apply creates (the manifest), and after_hash is empty
-- for a removal. temp_name is the temporary file the change goes through,
-- relative to the project folder, empty for a removal. prev_after_hash is
-- set while a change the run already wrote is announced again: it holds the
-- hash that change left, which the record goes back to if the new change
-- did not happen.
CREATE TABLE apply_file (
    apply_id        bigint NOT NULL REFERENCES apply_run (id),
    file_path       text NOT NULL,
    before_hash     text NOT NULL CHECK (before_hash ~ '^([0-9a-f]{64})?$'),
    after_hash      text NOT NULL CHECK (after_hash ~ '^([0-9a-f]{64})?$'),
    temp_name       text NOT NULL DEFAULT '',
    written         boolean NOT NULL DEFAULT false,
    prev_after_hash text CHECK (prev_after_hash ~ '^([0-9a-f]{64})?$'),
    PRIMARY KEY (apply_id, file_path)
);
CREATE INDEX apply_file_open ON apply_file (apply_id) WHERE NOT written;

-- When an externalize bundle was applied and the path in OpenBao its value
-- went to. An applied bundle keeps its verdict and name.
ALTER TABLE bundle ADD COLUMN applied_at timestamptz;
ALTER TABLE bundle ADD COLUMN store_path text NOT NULL DEFAULT '';

-- The apply whose changes a scan was requested to confirm.
ALTER TABLE scan ADD COLUMN apply_id bigint REFERENCES apply_run (id);

-- A job runs a scan, or checks or runs an apply. Jobs that exist before
-- this migration are scan jobs.
ALTER TABLE job ADD COLUMN kind text NOT NULL DEFAULT 'scan' CHECK (kind IN ('scan', 'apply-check', 'apply'));
ALTER TABLE job ADD COLUMN apply_id bigint REFERENCES apply_run (id);
ALTER TABLE job ALTER COLUMN scan_id DROP NOT NULL;
ALTER TABLE job ADD CONSTRAINT job_kind_target CHECK
    ((kind = 'scan') = (scan_id IS NOT NULL) AND (kind = 'scan') = (apply_id IS NULL));
CREATE UNIQUE INDEX job_one_per_apply_kind ON job (apply_id, kind) WHERE apply_id IS NOT NULL;

ALTER TABLE audit_event DROP CONSTRAINT audit_event_action_check;

ALTER TABLE audit_event ADD CONSTRAINT audit_event_action_check CHECK (action IN ('verdict', 'move', 'reveal', 'apply'));

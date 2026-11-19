CREATE TABLE scan (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id     bigint NOT NULL REFERENCES project (id),
    status         text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'succeeded', 'failed')),
    requested_by   text NOT NULL,
    requested_at   timestamptz NOT NULL DEFAULT now(),
    started_at     timestamptz,
    finished_at    timestamptz,
    commit_hash    text NOT NULL DEFAULT '',
    rules_version  text NOT NULL DEFAULT '',
    tool_versions  jsonb NOT NULL DEFAULT '{}',
    failure_reason text NOT NULL DEFAULT '',
    files          integer NOT NULL DEFAULT 0,
    skipped        integer NOT NULL DEFAULT 0,
    findings       integer NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX scan_one_active_per_project ON scan (project_id) WHERE status IN ('queued', 'running');

CREATE INDEX scan_project_id ON scan (project_id, id DESC);

CREATE TABLE job (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    scan_id      bigint NOT NULL UNIQUE REFERENCES scan (id),
    state        text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'done', 'failed')),
    attempts     integer NOT NULL DEFAULT 0,
    locked_by    text,
    locked_until timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX job_open ON job (state, locked_until) WHERE state IN ('queued', 'running');

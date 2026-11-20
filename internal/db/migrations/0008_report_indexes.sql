-- The runs of a project, newest first, and the audit events of a project,
-- newest first: what a report and the project page read.
CREATE INDEX apply_run_project_id ON apply_run (project_id, id DESC);
CREATE INDEX audit_event_project_id ON audit_event (project_id, id DESC);

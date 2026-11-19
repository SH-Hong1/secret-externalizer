-- A set of findings in one project that share a fingerprint. It carries the
-- verdict and the placeholder name a user gave them, never a value.
CREATE TABLE bundle (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    project_id  bigint NOT NULL REFERENCES project (id),
    fingerprint text NOT NULL CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    -- shared: every finding of the fingerprint joins it; frontend: the same
    -- for JSP, JS and HTML files; own: only what a user or the rule for
    -- common values put here.
    origin      text NOT NULL CHECK (origin IN ('shared', 'frontend', 'own')),
    verdict     text NOT NULL DEFAULT '' CHECK (verdict IN ('', 'externalize', 'redact', 'false-positive', 'hold')),
    hold_reason text NOT NULL DEFAULT '' CHECK (hold_reason IN ('', 'structure-change', 'needs-confirmation', 'later')),
    name        text CHECK (name ~ '^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*){1,4}$'),
    note        text NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
    decided_by  text NOT NULL DEFAULT '',
    decided_at  timestamptz,
    CHECK ((verdict = 'externalize') = (name IS NOT NULL)),
    CHECK ((verdict = 'hold') = (hold_reason <> '')),
    UNIQUE (project_id, name)
);
CREATE UNIQUE INDEX bundle_one_auto ON bundle (project_id, fingerprint, origin) WHERE origin <> 'own';
CREATE INDEX bundle_fingerprint ON bundle (fingerprint);

ALTER TABLE finding ADD COLUMN bundle_id bigint REFERENCES bundle (id);

-- Customer data, hosts, ports and accounts start in a bundle of their own.
DO $$
DECLARE
    f record;
    b bigint;
BEGIN
    FOR f IN
        SELECT id, project_id, fingerprint FROM finding
        WHERE category = 'customer-data' OR kind IN ('host', 'port', 'account')
        ORDER BY id
    LOOP
        INSERT INTO bundle (project_id, fingerprint, origin)
        VALUES (f.project_id, f.fingerprint, 'own')
        RETURNING id INTO b;
        UPDATE finding SET bundle_id = b WHERE id = f.id;
    END LOOP;
END
$$;

-- Every other finding joins the bundle of its fingerprint, with JSP, JS and
-- HTML files apart from the rest.
CREATE TEMPORARY TABLE bundle_backfill ON COMMIT DROP AS
SELECT id, project_id, fingerprint,
    CASE WHEN lower(file_path) ~ '\.(jsp|jspf|js|html|htm)$' THEN 'frontend' ELSE 'shared' END AS origin
FROM finding WHERE bundle_id IS NULL;

INSERT INTO bundle (project_id, fingerprint, origin)
SELECT DISTINCT project_id, fingerprint, origin FROM bundle_backfill;

UPDATE finding f SET bundle_id = b.id
FROM bundle_backfill n JOIN bundle b
    ON b.project_id = n.project_id AND b.fingerprint = n.fingerprint AND b.origin = n.origin
WHERE f.id = n.id;

ALTER TABLE finding ALTER COLUMN bundle_id SET NOT NULL;

CREATE INDEX finding_bundle_id ON finding (bundle_id);

-- What a user did to a project's findings. It refers to the project only, so
-- the record stays when a bundle or finding is gone. detail holds the
-- decision or the target of a move, never a value.
CREATE TABLE audit_event (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at            timestamptz NOT NULL DEFAULT now(),
    actor_subject text NOT NULL,
    actor_name    text NOT NULL,
    action        text NOT NULL CHECK (action IN ('verdict', 'move', 'reveal')),
    project_id    bigint NOT NULL REFERENCES project (id),
    bundle_id     bigint,
    finding_id    bigint,
    detail        jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(detail) = 'object')
);

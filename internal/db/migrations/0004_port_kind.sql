-- A connection string is stored as its components, and a port is one of
-- them.
ALTER TABLE finding DROP CONSTRAINT finding_kind_check;

ALTER TABLE finding ADD CONSTRAINT finding_kind_check CHECK (kind IN
    ('phone', 'rrn', 'email', 'card', 'address',
     'password', 'token', 'account', 'host', 'port', 'url', 'private-key', 'key-file', 'other'));

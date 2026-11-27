-- The fingerprint of the text of a finding's key, the one a masked key is
-- built from, or '' for a row without a key. A key that moves between its
-- text and its masked form across scans is the same finding when the two
-- fingerprints match. A masked key holds its fingerprint already; a key
-- stored as text before this column gets its fingerprint from the next scan
-- that reads it.
ALTER TABLE finding ADD COLUMN key_fingerprint text NOT NULL DEFAULT ''
    CHECK (key_fingerprint ~ '^([0-9a-f]{64})?$');

UPDATE finding SET key_fingerprint = substr(key, 2) WHERE key ~ '^#[0-9a-f]{64}$';

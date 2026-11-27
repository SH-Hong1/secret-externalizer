-- A key that would hold a value found in the scan is stored masked: '#'
-- followed by the fingerprint of the key's text, which finding.MaskedKey
-- builds. A configuration key never holds '#', so the two forms do not meet.
ALTER TABLE finding DROP CONSTRAINT finding_key_check;
ALTER TABLE finding ADD CONSTRAINT finding_key_check
    CHECK (key ~ '^[A-Za-z0-9_.-]{0,200}$' OR key ~ '^#[0-9a-f]{64}$');

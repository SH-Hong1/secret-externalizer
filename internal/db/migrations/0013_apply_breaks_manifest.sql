-- An apply skips the places in the manifest whose replacement would leave
-- a manifest the tool cannot read.
ALTER TABLE apply_item DROP CONSTRAINT apply_item_reason_check;
ALTER TABLE apply_item ADD CONSTRAINT apply_item_reason_check CHECK (reason IN
    ('', 'file-changed', 'file-missing', 'not-text', 'value-mismatch', 'value-unclear', 'values-differ',
     'not-allowed', 'overlap', 'untracked-irreversible', 'nested-repository', 'breaks-manifest',
     'key-in-source-file', 'key-holds-customer-data', 'holds-customer-data', 'file-removed', 'too-large',
     'store-failed', 'write-failed', 'interrupted'));

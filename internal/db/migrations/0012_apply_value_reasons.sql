-- An apply skips a place whose text alone does not settle the value it
-- stands for, and the places of one store key whose texts stand for
-- different values.
ALTER TABLE apply_item DROP CONSTRAINT apply_item_reason_check;
ALTER TABLE apply_item ADD CONSTRAINT apply_item_reason_check CHECK (reason IN
    ('', 'file-changed', 'file-missing', 'not-text', 'value-mismatch', 'value-unclear', 'values-differ',
     'not-allowed', 'overlap', 'untracked-irreversible', 'nested-repository', 'key-in-source-file',
     'key-holds-customer-data', 'holds-customer-data', 'file-removed', 'too-large', 'store-failed',
     'write-failed', 'interrupted'));

-- 0039_barcode_batches.sql — External barcode batch import (feature #146).
-- barcode_batches: holds CSV/PDF batch uploads pending operator approval.
-- barcode_batch_entries: individual barcode rows parsed from the batch file.
-- Status lifecycle: uploaded → pending_approval → active | rejected

-- +goose Up

CREATE TABLE barcode_batches (
    id            uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    allocation_id uuid        REFERENCES external_allocations(id) ON DELETE SET NULL,
    source        text        NOT NULL CONSTRAINT barcode_batches_source_check
                              CHECK (source IN ('csv', 'pdf')),
    status        text        NOT NULL DEFAULT 'uploaded'
                              CONSTRAINT barcode_batches_status_check
                              CHECK (status IN ('uploaded', 'pending_approval', 'active', 'rejected')),
    filename      text        NOT NULL,
    row_count     integer     NOT NULL DEFAULT 0,
    authority_id  uuid        REFERENCES barcode_authorities(id),
    notes         text,
    uploaded_by   text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX barcode_batches_allocation_id ON barcode_batches (allocation_id)
    WHERE allocation_id IS NOT NULL;

CREATE INDEX barcode_batches_status ON barcode_batches (status);

COMMENT ON TABLE barcode_batches IS
    'CSV/PDF barcode batch uploads. Each batch must be approved by a platform_operator '
    'before its barcodes are registered in the barcodes table for scanning. Feature #146.';

CREATE TABLE barcode_batch_entries (
    id           uuid        NOT NULL DEFAULT uuidv7() PRIMARY KEY,
    batch_id     uuid        NOT NULL REFERENCES barcode_batches(id) ON DELETE CASCADE,
    external_ref text        NOT NULL,
    status       text        NOT NULL DEFAULT 'pending'
                             CONSTRAINT barcode_batch_entries_status_check
                             CHECK (status IN ('pending', 'active', 'rejected')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (batch_id, external_ref)
);

CREATE INDEX barcode_batch_entries_batch_id ON barcode_batch_entries (batch_id);

COMMENT ON TABLE barcode_batch_entries IS
    'Individual barcode values parsed from a barcode_batches upload. '
    'Status mirrors the parent batch on approve/reject. Feature #146.';

INSERT INTO roles (name, description) VALUES
    ('platform_operator',
     'Platform operator with authority to approve or reject external barcode batch imports')
ON CONFLICT DO NOTHING;

INSERT INTO permissions (name, description) VALUES
    ('barcode_batch.upload',  'Upload a CSV barcode batch for review (feature #146)'),
    ('barcode_batch.approve', 'Approve or reject a barcode batch (platform_operator only, feature #146)'),
    ('barcode_batch.read',    'Read barcode batch details and entries (feature #146)')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'admin'
  AND  p.name IN ('barcode_batch.upload', 'barcode_batch.approve', 'barcode_batch.read')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'org_admin'
  AND  p.name IN ('barcode_batch.upload', 'barcode_batch.read')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions (role_id, permission_id)
SELECT r.id, p.id
FROM   roles r CROSS JOIN permissions p
WHERE  r.name = 'platform_operator'
  AND  p.name IN ('barcode_batch.upload', 'barcode_batch.approve', 'barcode_batch.read')
ON CONFLICT DO NOTHING;

-- +goose Down

DELETE FROM role_permissions
WHERE permission_id IN (
    SELECT id FROM permissions
    WHERE name IN ('barcode_batch.upload', 'barcode_batch.approve', 'barcode_batch.read')
);
DELETE FROM permissions
WHERE name IN ('barcode_batch.upload', 'barcode_batch.approve', 'barcode_batch.read');
DELETE FROM roles WHERE name = 'platform_operator';
DROP TABLE IF EXISTS barcode_batch_entries;
DROP TABLE IF EXISTS barcode_batches;

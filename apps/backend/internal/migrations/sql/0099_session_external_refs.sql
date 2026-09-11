-- 0099_session_external_refs.sql — W1-E1a (feature #523, event-bundle spec §6).
--
-- Maps a caller-supplied stable external reference (e.g. a WordPress
-- product: "wp:lampyris-staging:product:4711") to the arena session it
-- created, so a `source=arena` event bundle posted a second time is
-- recognised as an EDIT of the same session rather than creating a
-- duplicate. One session carries at most one ref (the unique index on
-- session_id); one ref inside an organization points at at most one
-- session (the composite primary key). The same ref string is free to be
-- reused by a different organization — refs are namespaced by the calling
-- site, not globally unique.

-- +goose Up
CREATE TABLE session_external_refs (
    org_id       uuid NOT NULL REFERENCES organizations(id),
    external_ref text NOT NULL CHECK (length(external_ref) BETWEEN 1 AND 200),
    session_id   uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, external_ref)
);

CREATE UNIQUE INDEX session_external_refs_session_uq ON session_external_refs (session_id);

COMMENT ON TABLE session_external_refs IS
    'W1-E: stable external reference (externalRef) of a session created '
    'through an arena-native event bundle import. Written in the same '
    'transaction as the session so a repeated bundle with the same '
    'externalRef resolves to the existing session (edit) instead of '
    'creating a duplicate. PK (org_id, external_ref) scopes refs per '
    'organization; session_external_refs_session_uq keeps at most one ref '
    'per session. A mismatch between the ref and an explicitly supplied '
    'actionEventId is reported as import.external_ref_conflict (409).';

-- +goose Down
DROP INDEX IF EXISTS session_external_refs_session_uq;
DROP TABLE IF EXISTS session_external_refs;

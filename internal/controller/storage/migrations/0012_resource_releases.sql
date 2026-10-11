-- A release is deliberately separate from desired state. It fences stale
-- GitOps files and operation submissions until an explicit adoption reclaims
-- the resource; neither a release nor its removal changes infrastructure.
CREATE TABLE resource_releases (
    api_version TEXT NOT NULL,
    kind TEXT NOT NULL,
    name TEXT NOT NULL,
    manifest_digest TEXT NOT NULL,
    released_at TEXT NOT NULL,
    PRIMARY KEY (api_version, kind, name)
);

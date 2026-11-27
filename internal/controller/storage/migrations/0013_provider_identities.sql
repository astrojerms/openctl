CREATE TABLE provider_identities (
    name TEXT PRIMARY KEY,
    context TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    node TEXT NOT NULL,
    vmid INTEGER NOT NULL CHECK(vmid > 0),
    identity TEXT NOT NULL,
    UNIQUE(endpoint, node, vmid)
);

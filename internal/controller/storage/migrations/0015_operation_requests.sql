-- Request bindings outlive operation history. Deliberately no foreign key:
-- pruning an operation must never make its accepted request ID reusable.
CREATE TABLE operation_requests (
    request_id TEXT NOT NULL PRIMARY KEY,
    actor TEXT NOT NULL,
    intent_hash TEXT NOT NULL,
    operation_id TEXT NOT NULL UNIQUE
);

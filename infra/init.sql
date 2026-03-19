CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS rules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id VARCHAR NOT NULL,
    resource VARCHAR NOT NULL,
    limit_count INTEGER NOT NULL,
    window_seconds INTEGER NOT NULL,
    created_at TIMESTAMP DEFAULT NOW(),
    updated_at TIMESTAMP DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS audit_log (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id VARCHAR NOT NULL,
    resource VARCHAR NOT NULL,
    status VARCHAR NOT NULL,
    timestamp TIMESTAMP DEFAULT NOW(),
    remaining_quota INTEGER,
    correlation_id VARCHAR
);


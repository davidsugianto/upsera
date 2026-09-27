-- Phase 1 schema. Every object lives in the "upsera" schema, never "public",
-- so Supabase's Data API cannot expose it. Table names are always
-- schema-qualified so queries work through any pooler mode.

-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    -- Supabase roles: make sure they can never read this schema, even if a
    -- project exposes new schemas or grants default privileges.
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'anon') THEN
        EXECUTE 'REVOKE ALL ON SCHEMA upsera FROM anon';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'authenticated') THEN
        EXECUTE 'REVOKE ALL ON SCHEMA upsera FROM authenticated';
    END IF;
END
$$;
-- +goose StatementEnd

CREATE TABLE upsera.users (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    email         text        NOT NULL,
    name          text        NOT NULL DEFAULT '',
    password_hash text        NOT NULL,
    is_admin      boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_key ON upsera.users (lower(email));

CREATE TABLE upsera.teams (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE upsera.memberships (
    team_id    bigint      NOT NULL REFERENCES upsera.teams (id) ON DELETE CASCADE,
    user_id    bigint      NOT NULL REFERENCES upsera.users (id) ON DELETE CASCADE,
    role       text        NOT NULL CHECK (role IN ('owner', 'editor', 'viewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id)
);
CREATE INDEX memberships_user_idx ON upsera.memberships (user_id);

CREATE TABLE upsera.sessions (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    token_hash   bytea       NOT NULL UNIQUE,
    user_id      bigint      NOT NULL REFERENCES upsera.users (id) ON DELETE CASCADE,
    csrf_token   text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);
CREATE INDEX sessions_expires_idx ON upsera.sessions (expires_at);

CREATE TABLE upsera.api_tokens (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id      bigint      NOT NULL REFERENCES upsera.teams (id) ON DELETE CASCADE,
    name         text        NOT NULL,
    scope        text        NOT NULL CHECK (scope IN ('read', 'write')),
    token_hash   bytea       NOT NULL UNIQUE,
    created_by   bigint      REFERENCES upsera.users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz,
    expires_at   timestamptz,
    revoked_at   timestamptz
);
CREATE INDEX api_tokens_team_idx ON upsera.api_tokens (team_id);

-- Tokens are soft-deleted (revoked_at set, never hard-deleted) so audit_log
-- rows always keep a valid actor_token_id.
CREATE TABLE upsera.audit_log (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id        bigint      REFERENCES upsera.teams (id) ON DELETE CASCADE,
    actor_user_id  bigint      REFERENCES upsera.users (id) ON DELETE SET NULL,
    actor_token_id bigint      REFERENCES upsera.api_tokens (id),
    action         text        NOT NULL,
    target_type    text        NOT NULL,
    target_id      bigint,
    details        jsonb       NOT NULL DEFAULT '{}',
    at             timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_team_idx ON upsera.audit_log (team_id, id DESC);
CREATE INDEX audit_log_actor_token_idx ON upsera.audit_log (actor_token_id);

-- The server's built-in checker is the probe named "local", so every
-- heartbeat carries a probe_id.
CREATE TABLE upsera.probes (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name         text        NOT NULL UNIQUE,
    region       text        NOT NULL,
    token_hash   bytea       UNIQUE,
    last_seen_at timestamptz,
    version      text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);
INSERT INTO upsera.probes (name, region) VALUES ('local', 'local');

CREATE TABLE upsera.monitors (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id          bigint      NOT NULL REFERENCES upsera.teams (id) ON DELETE CASCADE,
    name             text        NOT NULL CHECK (name <> ''),
    type             text        NOT NULL CHECK (type IN ('http', 'keyword', 'tcp', 'ping', 'dns', 'push')),
    config           jsonb       NOT NULL DEFAULT '{}',
    interval_s       integer     NOT NULL CHECK (interval_s BETWEEN 20 AND 86400),
    retry_interval_s integer     NOT NULL CHECK (retry_interval_s BETWEEN 20 AND 86400),
    retries          integer     NOT NULL CHECK (retries BETWEEN 0 AND 10),
    timeout_s        integer     NOT NULL CHECK (timeout_s >= 1 AND timeout_s < interval_s),
    paused           boolean     NOT NULL DEFAULT false,
    group_name       text        NOT NULL DEFAULT '',
    tags             text[]      NOT NULL DEFAULT '{}',
    push_token       text        UNIQUE,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CHECK ((type = 'push') = (push_token IS NOT NULL))
);
CREATE INDEX monitors_team_idx ON upsera.monitors (team_id);

-- status: 0 down, 1 up, 2 pending, 3 maintenance (model.Status).
CREATE TABLE upsera.heartbeats (
    monitor_id bigint      NOT NULL REFERENCES upsera.monitors (id) ON DELETE CASCADE,
    probe_id   bigint      NOT NULL REFERENCES upsera.probes (id) ON DELETE CASCADE,
    time       timestamptz NOT NULL,
    status     smallint    NOT NULL,
    latency_ms integer     NOT NULL,
    message    text        NOT NULL DEFAULT ''
);
CREATE INDEX heartbeats_monitor_time_idx ON upsera.heartbeats (monitor_id, time DESC);
-- Retention pruning scans by time; BRIN is tiny for append-only data.
CREATE INDEX heartbeats_time_brin ON upsera.heartbeats USING brin (time);

CREATE TABLE upsera.monitor_state (
    monitor_id           bigint      PRIMARY KEY REFERENCES upsera.monitors (id) ON DELETE CASCADE,
    status               smallint    NOT NULL,
    since                timestamptz NOT NULL,
    last_check_at        timestamptz NOT NULL,
    consecutive_failures integer     NOT NULL DEFAULT 0,
    flap_count           integer     NOT NULL DEFAULT 0,
    last_dedupe_key      text        NOT NULL DEFAULT '',
    tls_expires_at       timestamptz,
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE upsera.uptime_daily (
    monitor_id     bigint  NOT NULL REFERENCES upsera.monitors (id) ON DELETE CASCADE,
    day            date    NOT NULL,
    checks         integer NOT NULL,
    up             integer NOT NULL,
    avg_latency_ms integer NOT NULL,
    p95_latency_ms integer NOT NULL,
    PRIMARY KEY (monitor_id, day)
);

CREATE TABLE upsera.instance_settings (
    id                    integer     PRIMARY KEY CHECK (id = 1),
    block_private_targets boolean     NOT NULL DEFAULT false,
    retention_days        integer     CHECK (retention_days >= 3),
    updated_at            timestamptz NOT NULL DEFAULT now()
);
INSERT INTO upsera.instance_settings (id) VALUES (1);

-- +goose Down
DROP TABLE upsera.instance_settings;
DROP TABLE upsera.uptime_daily;
DROP TABLE upsera.monitor_state;
DROP TABLE upsera.heartbeats;
DROP TABLE upsera.monitors;
DROP TABLE upsera.probes;
DROP TABLE upsera.audit_log;
DROP TABLE upsera.api_tokens;
DROP TABLE upsera.sessions;
DROP TABLE upsera.memberships;
DROP TABLE upsera.teams;
DROP TABLE upsera.users;

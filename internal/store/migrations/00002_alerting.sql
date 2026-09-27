-- Phase 2: alerting. Notification channels (config encrypted at rest with
-- secret.Box, "v1:" prefix), escalation policies, monitor dependencies and
-- channel links, alerts, the notification send log and maintenance windows.

-- +goose Up
CREATE TABLE upsera.notification_channels (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id    bigint      NOT NULL REFERENCES upsera.teams (id) ON DELETE CASCADE,
    type       text        NOT NULL CHECK (type IN ('telegram', 'discord', 'slack', 'slack_app', 'smtp', 'webhook')),
    name       text        NOT NULL CHECK (name <> ''),
    config     text        NOT NULL, -- secret.Box ciphertext "v1:..." of the whole JSON config
    is_default boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX notification_channels_team_idx ON upsera.notification_channels (team_id);

CREATE TABLE upsera.escalation_policies (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id    bigint      NOT NULL REFERENCES upsera.teams (id) ON DELETE CASCADE,
    name       text        NOT NULL CHECK (name <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One row per (step, channel); delay_s is repeated on every row of a step.
CREATE TABLE upsera.escalation_steps (
    policy_id  bigint   NOT NULL REFERENCES upsera.escalation_policies (id) ON DELETE CASCADE,
    position   smallint NOT NULL CHECK (position BETWEEN 0 AND 9),
    delay_s    integer  NOT NULL CHECK (delay_s BETWEEN 10 AND 86400),
    channel_id bigint   NOT NULL REFERENCES upsera.notification_channels (id) ON DELETE RESTRICT,
    PRIMARY KEY (policy_id, position, channel_id)
);
CREATE INDEX escalation_steps_channel_idx ON upsera.escalation_steps (channel_id);

ALTER TABLE upsera.monitors
    ADD COLUMN parent_id bigint REFERENCES upsera.monitors (id) ON DELETE SET NULL,
    ADD COLUMN escalation_policy_id bigint REFERENCES upsera.escalation_policies (id) ON DELETE RESTRICT,
    ADD CHECK (parent_id IS NULL OR parent_id <> id);
CREATE INDEX monitors_parent_idx ON upsera.monitors (parent_id);

CREATE TABLE upsera.monitor_channels (
    monitor_id bigint NOT NULL REFERENCES upsera.monitors (id) ON DELETE CASCADE,
    channel_id bigint NOT NULL REFERENCES upsera.notification_channels (id) ON DELETE CASCADE,
    PRIMARY KEY (monitor_id, channel_id)
);

-- Dedupe lives in alerts / notification_log.
ALTER TABLE upsera.monitor_state DROP COLUMN last_dedupe_key;

CREATE TABLE upsera.alerts (
    id                   uuid PRIMARY KEY,
    team_id              bigint      NOT NULL REFERENCES upsera.teams (id) ON DELETE CASCADE,
    monitor_id           bigint      NOT NULL REFERENCES upsera.monitors (id) ON DELETE CASCADE,
    incident_start       timestamptz NOT NULL,
    opened_at            timestamptz NOT NULL,
    message              text        NOT NULL DEFAULT '',
    step                 integer     NOT NULL DEFAULT -1,
    next_escalation_at   timestamptz,
    notified_channel_ids bigint[]    NOT NULL DEFAULT '{}',
    suppressed           boolean     NOT NULL DEFAULT false,
    flapping             boolean     NOT NULL DEFAULT false,
    acked_at             timestamptz,
    acked_by_user_id     bigint REFERENCES upsera.users (id) ON DELETE SET NULL,
    ack_source           text CHECK (ack_source IN ('web', 'slack', 'telegram')),
    acked_by_name        text        NOT NULL DEFAULT '',
    resolved_at          timestamptz,
    resolution           text        NOT NULL DEFAULT '' CHECK (resolution IN ('', 'recovered', 'maintenance')),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX alerts_open_monitor_key ON upsera.alerts (monitor_id) WHERE resolved_at IS NULL;
CREATE INDEX alerts_team_idx ON upsera.alerts (team_id, opened_at DESC);

CREATE TABLE upsera.notification_log (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id    bigint      NOT NULL REFERENCES upsera.teams (id) ON DELETE CASCADE,
    channel_id bigint REFERENCES upsera.notification_channels (id) ON DELETE SET NULL,
    monitor_id bigint REFERENCES upsera.monitors (id) ON DELETE SET NULL,
    alert_id   uuid REFERENCES upsera.alerts (id) ON DELETE SET NULL,
    event      text        NOT NULL CHECK (event IN ('down', 'recovered', 'flapping', 'cert_expiry', 'test')),
    dedupe_key text        NOT NULL,
    attempt    smallint    NOT NULL,
    ok         boolean     NOT NULL,
    error      text        NOT NULL DEFAULT '',
    at         timestamptz NOT NULL
);
CREATE INDEX notification_log_team_idx ON upsera.notification_log (team_id, id DESC);
CREATE INDEX notification_log_cert_idx ON upsera.notification_log (at) WHERE event = 'cert_expiry' AND ok;
CREATE INDEX notification_log_at_brin ON upsera.notification_log USING brin (at);

CREATE TABLE upsera.maintenance_windows (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id    bigint      NOT NULL REFERENCES upsera.teams (id) ON DELETE CASCADE,
    name       text        NOT NULL CHECK (name <> ''),
    starts_at  timestamptz NOT NULL,
    ends_at    timestamptz NOT NULL CHECK (ends_at > starts_at),
    recurrence text        NOT NULL DEFAULT 'none' CHECK (recurrence IN ('none', 'daily', 'weekly')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE upsera.maintenance_window_monitors (
    window_id  bigint NOT NULL REFERENCES upsera.maintenance_windows (id) ON DELETE CASCADE,
    monitor_id bigint NOT NULL REFERENCES upsera.monitors (id) ON DELETE CASCADE,
    PRIMARY KEY (window_id, monitor_id)
);

-- +goose Down
DROP TABLE upsera.maintenance_window_monitors;
DROP TABLE upsera.maintenance_windows;
DROP TABLE upsera.notification_log;
DROP TABLE upsera.alerts;
ALTER TABLE upsera.monitor_state ADD COLUMN last_dedupe_key text NOT NULL DEFAULT '';
DROP TABLE upsera.monitor_channels;
DROP INDEX upsera.monitors_parent_idx;
ALTER TABLE upsera.monitors DROP COLUMN escalation_policy_id, DROP COLUMN parent_id;
DROP TABLE upsera.escalation_steps;
DROP TABLE upsera.escalation_policies;
DROP TABLE upsera.notification_channels;

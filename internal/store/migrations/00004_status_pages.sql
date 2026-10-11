-- Phase 5: status pages. Public pages built from a team's monitors, incidents
-- (manual or auto-drafted from outages), email/webhook subscribers with a
-- restart-safe delivery outbox, and the instance SMTP settings used for
-- subscriber email (secret.Box ciphertext).

-- +goose Up
CREATE TABLE upsera.status_pages (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id       bigint      NOT NULL REFERENCES upsera.teams (id) ON DELETE CASCADE,
    slug          text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
    title         text        NOT NULL CHECK (title <> ''),
    description   text        NOT NULL DEFAULT '',
    logo_url      text        NOT NULL DEFAULT '',
    theme         text        NOT NULL DEFAULT 'auto' CHECK (theme IN ('auto', 'light', 'dark')),
    custom_domain text        UNIQUE CHECK (custom_domain <> ''),
    published     boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX status_pages_team_idx ON upsera.status_pages (team_id);

CREATE TABLE upsera.status_page_items (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    page_id      bigint  NOT NULL REFERENCES upsera.status_pages (id) ON DELETE CASCADE,
    section      text    NOT NULL DEFAULT '',
    monitor_id   bigint  NOT NULL REFERENCES upsera.monitors (id) ON DELETE CASCADE,
    display_name text    NOT NULL CHECK (display_name <> ''),
    position     integer NOT NULL,
    UNIQUE (page_id, monitor_id)
);
CREATE INDEX status_page_items_monitor_idx ON upsera.status_page_items (monitor_id);

CREATE TABLE upsera.incidents (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    page_id       bigint      NOT NULL REFERENCES upsera.status_pages (id) ON DELETE CASCADE,
    title         text        NOT NULL CHECK (title <> ''),
    severity      text        NOT NULL CHECK (severity IN ('minor', 'major', 'critical')),
    status        text        NOT NULL CHECK (status IN ('investigating', 'identified', 'monitoring', 'resolved')),
    draft         boolean     NOT NULL,
    auto_alert_id uuid        REFERENCES upsera.alerts (id) ON DELETE SET NULL,
    started_at    timestamptz NOT NULL,
    resolved_at   timestamptz,
    published_at  timestamptz,
    -- An auto-drafted incident deleted while its alert is still open is kept
    -- hidden, so the worker does not draft the same outage again.
    dismissed     boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX incidents_page_idx ON upsera.incidents (page_id, started_at DESC);
CREATE UNIQUE INDEX incidents_auto_alert_key ON upsera.incidents (page_id, auto_alert_id) WHERE auto_alert_id IS NOT NULL;

CREATE TABLE upsera.incident_updates (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    incident_id bigint      NOT NULL REFERENCES upsera.incidents (id) ON DELETE CASCADE,
    status      text        NOT NULL CHECK (status IN ('investigating', 'identified', 'monitoring', 'resolved')),
    body        text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX incident_updates_incident_idx ON upsera.incident_updates (incident_id, id);

CREATE TABLE upsera.subscribers (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    page_id    bigint      NOT NULL REFERENCES upsera.status_pages (id) ON DELETE CASCADE,
    kind       text        NOT NULL CHECK (kind IN ('email', 'webhook')),
    target     text        NOT NULL CHECK (target <> ''),
    confirmed  boolean     NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX subscribers_target_key ON upsera.subscribers (page_id, kind, lower(target));

-- Restart-safe delivery outbox: confirmation mails and incident notices.
CREATE TABLE upsera.subscriber_deliveries (
    id              bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    subscriber_id   bigint      NOT NULL REFERENCES upsera.subscribers (id) ON DELETE CASCADE,
    kind            text        NOT NULL CHECK (kind IN ('confirm', 'incident')),
    incident_id     bigint      REFERENCES upsera.incidents (id) ON DELETE CASCADE,
    update_id       bigint      REFERENCES upsera.incident_updates (id) ON DELETE CASCADE,
    state           text        NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'sent', 'failed')),
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    last_error      text        NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz
);
CREATE INDEX subscriber_deliveries_due_idx ON upsera.subscriber_deliveries (next_attempt_at) WHERE state = 'pending';
-- Foreign keys: cascading deletes (unsubscribe, incident/page deletion, the
-- hourly purge) find a row's deliveries through these.
CREATE INDEX subscriber_deliveries_subscriber_idx ON upsera.subscriber_deliveries (subscriber_id);
CREATE INDEX subscriber_deliveries_incident_idx ON upsera.subscriber_deliveries (incident_id) WHERE incident_id IS NOT NULL;
CREATE INDEX subscriber_deliveries_update_idx ON upsera.subscriber_deliveries (update_id) WHERE update_id IS NOT NULL;
-- Counting recent confirmation mails (the public sign-up caps).
CREATE INDEX subscriber_deliveries_confirm_idx ON upsera.subscriber_deliveries (created_at) WHERE kind = 'confirm';

ALTER TABLE upsera.instance_settings ADD COLUMN smtp text; -- secret.Box ciphertext of model.SMTPSettings JSON

-- +goose Down
ALTER TABLE upsera.instance_settings DROP COLUMN smtp;
DROP TABLE upsera.subscriber_deliveries;
DROP TABLE upsera.subscribers;
DROP TABLE upsera.incident_updates;
DROP TABLE upsera.incidents;
DROP TABLE upsera.status_page_items;
DROP TABLE upsera.status_pages;

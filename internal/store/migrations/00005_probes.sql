-- Phase 6: multi-location probes. A probe is registered with a one-time
-- token (hash in registration_token_hash, expires at registration_expires_at)
-- that is exchanged for a credential (hash in the existing token_hash).
-- One probe per region. Monitors choose regions and a quorum; the per-region
-- state lives in monitor_state.regions (JSON map region -> state).

-- +goose Up
ALTER TABLE upsera.probes
    ADD COLUMN registration_token_hash bytea UNIQUE,
    ADD COLUMN registration_expires_at timestamptz,
    ADD CONSTRAINT probes_region_key UNIQUE (region);

ALTER TABLE upsera.monitors
    ADD COLUMN regions text[]   NOT NULL DEFAULT '{local}',
    ADD COLUMN quorum  smallint NOT NULL DEFAULT 1,
    ADD CONSTRAINT monitors_regions_check CHECK (cardinality(regions) BETWEEN 1 AND 10),
    ADD CONSTRAINT monitors_quorum_check CHECK (quorum >= 1 AND quorum <= cardinality(regions));

ALTER TABLE upsera.monitor_state
    ADD COLUMN regions jsonb NOT NULL DEFAULT '{}';

ALTER TABLE upsera.notification_log DROP CONSTRAINT notification_log_event_check;
ALTER TABLE upsera.notification_log ADD CONSTRAINT notification_log_event_check
    CHECK (event IN ('down', 'recovered', 'flapping', 'cert_expiry', 'test', 'probe_silent', 'probe_recovered'));

-- +goose Down
DELETE FROM upsera.notification_log WHERE event IN ('probe_silent', 'probe_recovered');
ALTER TABLE upsera.notification_log DROP CONSTRAINT notification_log_event_check;
ALTER TABLE upsera.notification_log ADD CONSTRAINT notification_log_event_check
    CHECK (event IN ('down', 'recovered', 'flapping', 'cert_expiry', 'test'));
ALTER TABLE upsera.monitor_state DROP COLUMN regions;
ALTER TABLE upsera.monitors
    DROP CONSTRAINT monitors_quorum_check,
    DROP CONSTRAINT monitors_regions_check,
    DROP COLUMN quorum,
    DROP COLUMN regions;
DELETE FROM upsera.probes WHERE name <> 'local';
ALTER TABLE upsera.probes
    DROP CONSTRAINT probes_region_key,
    DROP COLUMN registration_expires_at,
    DROP COLUMN registration_token_hash;

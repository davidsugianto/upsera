-- Phase 7: service level objectives. One SLO per monitor: a target (percent
-- of valid checks good over the last window_days calendar days) and the
-- burn-rate thresholds of its fast and slow multi-window alerts, whose firing
-- state persists so a restart never notifies again.
--
-- The notification log also learns the Phase 7 events: degraded /
-- latency_recovered (DEGRADED) and slo_burn / slo_burn_recovered.

-- +goose Up
CREATE TABLE upsera.slos (
    id                bigserial PRIMARY KEY,
    team_id           bigint           NOT NULL REFERENCES upsera.teams (id) ON DELETE CASCADE,
    monitor_id        bigint           NOT NULL UNIQUE REFERENCES upsera.monitors (id) ON DELETE CASCADE,
    target            numeric(6,3)     NOT NULL CHECK (target > 0 AND target < 100),
    window_days       integer          NOT NULL DEFAULT 30 CHECK (window_days BETWEEN 7 AND 90),
    fast_burn         double precision NOT NULL DEFAULT 14.4,
    slow_burn         double precision NOT NULL DEFAULT 6,
    fast_firing_since timestamptz,
    slow_firing_since timestamptz,
    created_at        timestamptz      NOT NULL DEFAULT now(),
    updated_at        timestamptz      NOT NULL DEFAULT now(),
    CONSTRAINT slos_burn_check CHECK (slow_burn >= 1 AND fast_burn >= slow_burn AND fast_burn <= 1000)
);
CREATE INDEX slos_team_idx ON upsera.slos (team_id);

ALTER TABLE upsera.notification_log DROP CONSTRAINT notification_log_event_check;
ALTER TABLE upsera.notification_log ADD CONSTRAINT notification_log_event_check
    CHECK (event IN ('down', 'recovered', 'flapping', 'cert_expiry', 'test', 'probe_silent', 'probe_recovered',
                     'degraded', 'latency_recovered', 'slo_burn', 'slo_burn_recovered'));

-- +goose Down
DELETE FROM upsera.notification_log
    WHERE event IN ('degraded', 'latency_recovered', 'slo_burn', 'slo_burn_recovered');
ALTER TABLE upsera.notification_log DROP CONSTRAINT notification_log_event_check;
ALTER TABLE upsera.notification_log ADD CONSTRAINT notification_log_event_check
    CHECK (event IN ('down', 'recovered', 'flapping', 'cert_expiry', 'test', 'probe_silent', 'probe_recovered'));
DROP TABLE upsera.slos;

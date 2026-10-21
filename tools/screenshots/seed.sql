-- Demo history for the documentation screenshots. Run with psql inside the
-- throwaway Postgres container, after `upsctl apply -f deploy/examples/demo.yaml`:
--   psql -v ON_ERROR_STOP=1 -v tz=<the server's TZ> -f seed.sql
-- It is idempotent for the demo monitors (it replaces their history).
--
-- Story: 30 days of checks every 5 minutes, mostly UP with latency noise.
--   website       a 20 minute outage some days ago (SLO 99.9: still met)
--   checkout-api  a 4 hour outage (SLO 99.9: missed) and a slow stretch right
--                 now, far above its 7 day baseline: status DEGRADED
--   others        quiet
-- The noise is a hash of (monitor, time), so reruns give the same data.

BEGIN;

CREATE TEMP TABLE demo (name text, step interval, base_ms int, spread_ms int) ON COMMIT DROP;
INSERT INTO demo VALUES
    ('website',        interval '5 minutes', 120, 60),
    ('docs',           interval '5 minutes', 180, 90),
    ('database',       interval '5 minutes',  12,  8),
    ('dns',            interval '5 minutes',  25, 15),
    ('nightly-backup', interval '1 hour',      0,  0),
    ('checkout-api',   interval '5 minutes', 230, 80);

-- Anchors, in the server's time zone. Outages sit inside the current month
-- (reports are monthly), a few days back when the month is old enough.
CREATE TEMP TABLE anchor ON COMMIT DROP AS
SELECT date_trunc('minute', now()) AS now_t,
       date_trunc('month', now() AT TIME ZONE :'tz') AT TIME ZONE :'tz' AS month_start;
CREATE TEMP TABLE outage ON COMMIT DROP AS
SELECT 'website' AS name,
       greatest(a.now_t - interval '12 days', a.month_start + (a.now_t - a.month_start) / 4) AS at,
       interval '20 minutes' AS len
FROM anchor a
UNION ALL
SELECT 'checkout-api',
       greatest(a.now_t - interval '5 days', a.month_start + (a.now_t - a.month_start) / 2),
       interval '4 hours'
FROM anchor a;

DELETE FROM upsera.heartbeats WHERE monitor_id IN (SELECT id FROM upsera.monitors m JOIN demo d ON d.name = m.name);
DELETE FROM upsera.alerts WHERE monitor_id IN (SELECT id FROM upsera.monitors m JOIN demo d ON d.name = m.name);

INSERT INTO upsera.heartbeats (monitor_id, probe_id, time, status, latency_ms, message)
SELECT m.id, (SELECT id FROM upsera.probes WHERE name = 'local'), g.t,
       s.status,
       CASE s.status WHEN 0 THEN 0 ELSE s.latency END,
       CASE s.status WHEN 0 THEN 'dial tcp: connection timed out' WHEN 4 THEN 'slow response' ELSE '' END
FROM demo d
JOIN upsera.monitors m ON m.name = d.name
CROSS JOIN anchor a
CROSS JOIN LATERAL generate_series(a.now_t - interval '30 days', a.now_t - interval '1 minute', d.step) AS g(t)
CROSS JOIN LATERAL (
    SELECT
        CASE
            WHEN EXISTS (SELECT 1 FROM outage o WHERE o.name = d.name AND g.t >= o.at AND g.t < o.at + o.len) THEN 0
            WHEN d.name = 'checkout-api' AND g.t >= a.now_t - interval '3 hours' THEN 4
            ELSE 1
        END AS status,
        (d.base_ms + (abs(hashtext(m.id::text || g.t::text)) % 1000) / 1000.0 * d.spread_ms)::int
            + CASE WHEN d.name = 'checkout-api' AND g.t >= a.now_t - interval '3 hours'
                   THEN 700 + (abs(hashtext('slow' || g.t::text)) % 400) ELSE 0 END AS latency
) s;

-- The alerts of the outages (resolved), as the alert engine would store them.
INSERT INTO upsera.alerts (id, team_id, monitor_id, incident_start, opened_at, message, resolved_at, resolution)
SELECT gen_random_uuid(), m.team_id, m.id, o.at, o.at + interval '2 minutes',
       'dial tcp: connection timed out', o.at + o.len, 'recovered'
FROM outage o JOIN upsera.monitors m ON m.name = o.name;

-- The live state of every demo monitor (the app is restarted after this).
INSERT INTO upsera.monitor_state (monitor_id, status, since, last_check_at, consecutive_failures, flap_count, tls_expires_at, updated_at)
SELECT m.id,
       CASE WHEN m.name = 'checkout-api' THEN 4 ELSE 1 END,
       CASE WHEN m.name = 'checkout-api' THEN a.now_t - interval '3 hours'
            ELSE coalesce((SELECT o.at + o.len FROM outage o WHERE o.name = m.name), a.now_t - interval '30 days') END,
       a.now_t - interval '1 minute', 0, 0,
       CASE WHEN m.type IN ('http', 'keyword') THEN a.now_t + interval '62 days' END,
       now()
FROM upsera.monitors m JOIN demo d ON d.name = m.name CROSS JOIN anchor a
ON CONFLICT (monitor_id) DO UPDATE SET status = EXCLUDED.status, since = EXCLUDED.since,
    last_check_at = EXCLUDED.last_check_at, consecutive_failures = 0, flap_count = 0,
    tls_expires_at = EXCLUDED.tls_expires_at, regions = '{}', updated_at = now();

-- uptime_daily the way the hourly rollup fills it (same statement as
-- internal/store/monitors.go rollupUpsertSQL).
INSERT INTO upsera.uptime_daily (monitor_id, day, checks, up, avg_latency_ms, p95_latency_ms)
SELECT h.monitor_id, (h.time AT TIME ZONE :'tz')::date,
    count(*) FILTER (WHERE h.status <> 3),
    count(*) FILTER (WHERE h.status IN (1, 2, 4)),
    coalesce(avg(h.latency_ms) FILTER (WHERE h.status IN (1, 4)), 0)::integer,
    coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY h.latency_ms) FILTER (WHERE h.status IN (1, 4)), 0)::integer
FROM upsera.heartbeats h
WHERE h.monitor_id IN (SELECT m.id FROM upsera.monitors m JOIN demo d ON d.name = m.name)
GROUP BY 1, 2
ON CONFLICT (monitor_id, day) DO UPDATE SET checks = EXCLUDED.checks, up = EXCLUDED.up,
    avg_latency_ms = EXCLUDED.avg_latency_ms, p95_latency_ms = EXCLUDED.p95_latency_ms;

-- Two registered remote probes, so Administration > Probes is not empty.
INSERT INTO upsera.probes (name, region, token_hash, last_seen_at, version)
SELECT v.name, v.region, decode(md5(v.name) || md5(v.name || 'demo'), 'hex'), now(), 'dev'
FROM (VALUES ('frankfurt', 'eu-central'), ('singapore', 'ap-southeast')) AS v(name, region)
ON CONFLICT (name) DO UPDATE SET last_seen_at = now();

COMMIT;

-- Phase 4: Docker monitoring. Docker hosts a team's docker monitors read
-- container state from. Monitors reference a host by id inside their JSON
-- config; deleting a host that a monitor still uses is refused by the store.

-- +goose Up
CREATE TABLE upsera.docker_hosts (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id    bigint      NOT NULL REFERENCES upsera.teams (id) ON DELETE CASCADE,
    name       text        NOT NULL CHECK (name <> ''),
    endpoint   text        NOT NULL CHECK (endpoint <> ''),
    tls        text        NOT NULL, -- secret.Box ciphertext "v1:..." of {"ca","cert","key"} PEM JSON
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (team_id, name)
);

ALTER TABLE upsera.monitors DROP CONSTRAINT monitors_type_check;
ALTER TABLE upsera.monitors ADD CONSTRAINT monitors_type_check
    CHECK (type IN ('http', 'keyword', 'tcp', 'ping', 'dns', 'push', 'docker'));

-- +goose Down
DELETE FROM upsera.monitors WHERE type = 'docker';
ALTER TABLE upsera.monitors DROP CONSTRAINT monitors_type_check;
ALTER TABLE upsera.monitors ADD CONSTRAINT monitors_type_check
    CHECK (type IN ('http', 'keyword', 'tcp', 'ping', 'dns', 'push'));
DROP TABLE upsera.docker_hosts;

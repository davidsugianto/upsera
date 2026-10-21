# Multi-location probes

By default every check runs on the server, in the built-in `local` region. To check from other places,
run `upsera-probe` on a VPS in each location.

A probe needs no open ports. It polls the server over HTTPS for the monitors assigned to its region, runs
the checks, and pushes the results back. A monitor can select several regions and a quorum, so one probe's
bad network does not page you.

![Administration, Probes: the built-in local probe and two remote probes, with their regions and status.](/screenshots/probes.png)

## Add a probe

1. In the dashboard open Administration, then Probes (instance admin only). Create a probe with a name
   and a region. A region is lowercase letters, digits and `-`, for example `sg-1`. `local` is reserved.
2. Copy the registration token (`upr_...`). It is shown once, is valid for 24 hours and works once.
3. On the remote host:

```sh
docker run -d --restart unless-stopped --name upsera-probe \
  -v upsera-probe:/data \
  -e SERVER_URL=https://status.example.com \
  -e PROBE_TOKEN=upr_xxxxxxxx \
  ghcr.io/davidsugianto/upsera-probe:latest
```

On first start the probe exchanges the token for a long-lived credential (`upp_...`) and keeps it in
`/data/credential` (mode 0600). Keep `/data` on a volume. After that `PROBE_TOKEN` is ignored.

The region comes from the probe's row on the server, so there is no `REGION` variable.

You can also build the image yourself with `make docker-build-probe` (`docker build --target probe`),
or the binary with `make probe`.

`upsera-probe version` prints the version. If the server rejects the credential (revoked or deleted), the
probe logs it and exits with a non-zero code.

## Probe settings

| Variable | Default | Meaning |
|---|---|---|
| `SERVER_URL` | required | Base URL of the Upsera server. |
| `PROBE_TOKEN` | empty | One-time registration token. Only needed on the first start. |
| `DATA_DIR` | `/data` | Where the credential is stored. The image sets `/data`. |
| `BUFFER_SIZE` | `10000` | Results kept in memory while the server is unreachable. The oldest are dropped first. |
| `MAX_CONCURRENT_CHECKS` | `50` | Checks running at once. |
| `ALLOW_INSECURE_HTTP` | unset | Must be `1` to use an `http://` `SERVER_URL`. Without it only HTTPS is accepted, because the credential and monitor configs travel over this connection. |
| `LOG_LEVEL` | `info` | Logging. |

On the server, `PROBE_SILENT_AFTER` (default `2m`, minimum `30s`) is how long a registered probe may go
unseen before it counts as silent.

## Rotate or remove a probe

To rotate or revoke a credential, issue a new registration token for the probe. The old credential stops
working at once. Or delete the probe. A region still used by a monitor cannot be deleted.

Deleting a probe also deletes the heartbeats it reported, so that region's latency history is gone. Daily
uptime figures that were already rolled up stay.

## Regions and quorum

In a monitor's form pick one or more regions and a quorum. The default quorum is a majority, so 2 of 3.
The `local` region is always available.

- Each region applies the monitor's retries to its own results.
- The monitor is `down` when at least `quorum` fresh regions are down.
- A region's result is fresh for twice the monitor interval.
- If fewer than `quorum` fresh regions are reporting, the monitor goes `pending` with a message such as
  `probe silent: 1 of 3 regions reporting (sg-1 silent)`. It does not go down, so a dead probe never opens
  a down alert by itself.
- Latency is charted per region on the monitor page.

Only HTTP, keyword, TCP, ping and DNS monitors can use remote regions. Push and Docker monitors run only on
the server.

## Probe silent alert

When a registered probe is not heard from for `PROBE_SILENT_AFTER`, each team that has an unpaused monitor
in that region gets a message on its default channels. It gets another one when the probe reports again.
There is no acknowledgement or escalation for it. A probe that was created but never registered is not
reported.

## Trust model

Probes receive the full config of the monitors in their region, including credentials placed in headers or
basic auth. Run probes only on hosts you trust.

A probe credential is separate from API tokens and cannot be used for anything but the probe endpoints. To
revoke a compromised probe, delete it or issue a new token, and rotate the credentials in those monitors if
the host was exposed. The `block_private_targets` instance setting also applies to probes.

## Storage

Each check from each region writes its own heartbeat row. A monitor with three regions stores three times
the heartbeats. See `HEARTBEAT_RETENTION_DAYS` in [configuration](./configuration).

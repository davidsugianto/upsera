# Monitors as code with upsctl

`upsctl` exports, diffs and applies a team's setup as YAML through the REST API. It covers channels,
escalation policies, monitors (with their SLO) and status pages. Use it from CI, for example.

## Install

```sh
make upsctl                          # builds the binary
docker build --target upsctl .       # builds the image
```

Released images are published as `ghcr.io/davidsugianto/upsctl`.

## Use

Set the server URL and a team token. `apply` needs a token with `write` scope. `diff` and `export` need `read`.

```sh
export UPSERA_URL=https://status.example.com UPSERA_TOKEN=ups_xxxxxxxx
upsctl export -o upsera.yaml          # live team -> YAML, secrets as placeholders
upsctl diff -f upsera.yaml            # what would change
upsctl apply -f upsera.yaml --yes     # create and update
upsctl apply -f upsera.yaml --yes --prune   # also delete what the file no longer lists
upsctl version
```

You can pass `--url` and `--token` instead of the environment variables. `-f` can be repeated, and the
files are merged.

| Command | What it does |
|---|---|
| `export [-o FILE]` | Writes the token's team as a spec, sorted by name (pages by slug), without ids or timestamps. |
| `diff -f FILE` | Prints `+` create and `~` update with the changed fields. With `--prune` it also prints `-` deletes. |
| `apply -f FILE [--prune] [--yes]` | Creates and updates channels, policies, monitors (parents first) and status pages, in that order. With `--prune` it then deletes what the file does not list. Without `--prune` those entries are listed as not managed and kept. |
| `version` | Prints the version. |

Every check runs before the first write. If an API call fails, `apply` stops and the lines above say what was
applied. Without `--yes`, `apply` asks for confirmation and refuses to run when stdin is not a terminal.

## Exit codes

`diff` uses its exit code to say what it found:

| Code | Meaning |
|---|---|
| `0` | Nothing would change. |
| `2` | Something would change. |
| `1` | Error. |

## The spec

A sample is in `deploy/examples/upsera.yaml`:

```yaml
version: 1

channels:
  - name: oncall-telegram
    type: telegram
    is_default: true
    config:
      chat_id: "-1001234567890"
      bot_token: ${UPSERA_ONCALL_TELEGRAM_BOT_TOKEN}

escalation_policies:
  - name: default
    steps:
      - channels: [oncall-telegram]
        delay_s: 600

monitors:
  - name: api
    type: http
    interval_s: 30
    timeout_s: 10
    escalation_policy: default
    config:
      url: https://api.example.com/health
      accepted_status: ["200-299"]
    slo:
      target: 99.9
      window_days: 30
```

Resources are matched by name inside the team (status pages by slug) and refer to each other by name.
`upsctl` refuses to run when a team has duplicate names.

## Secrets

`export` never writes secrets. It writes a placeholder named `${UPSERA_<CHANNEL>_<FIELD>}` instead.
`apply` and `diff` read the values from the environment.

- Placeholders go inside string values. Write `$$` for a literal `$`.
- Only variables whose names start with `UPSERA_` can be referenced.
- `UPSERA_TOKEN` and `UPSERA_URL` cannot be referenced, so a spec cannot read your API token.
- The server ignores a channel update that changes nothing, so repeated applies stay quiet.

## Things to know

- Push URLs are generated per instance, and push tokens are not exported.
- A status page's `custom_domain` can only be set by an instance admin session in the browser. `apply` skips it with a warning.
- Regions and Docker hosts named by a monitor must already exist.
- The monitor group field is `group_name`. Docker monitors use `docker_host` (the host's name) in `config`.
- Quote YAML names such as `n`, `no` or `on`. YAML 1.1 reads them as booleans.

## GitLab CI example

`deploy/examples/upsctl.gitlab-ci.yml` runs `upsctl diff` on merge requests and `upsctl apply` on the default
branch.

- Merge request pipelines on unprotected branches do not get protected variables, so the two jobs use
  different tokens: `UPSCTL_READ_TOKEN` (read scope, masked, not protected) for `diff` and
  `UPSCTL_WRITE_TOKEN` (write scope, masked and protected) for `apply`. Each job maps its token to
  `UPSERA_TOKEN`.
- Every `${...}` placeholder in the spec needs a masked, protected CI variable for `apply`.
- `diff` never compares secrets, so on merge requests the job fills each placeholder that has no value with
  `{}` instead of exposing the real secret. If a placeholder is for a non-secret field, define it as a plain
  (unprotected) variable so `diff` sees its real value.
- Exit code 2 from `diff` means changes are pending. The job passes with a warning
  (`allow_failure: exit_codes: [2]`).
- The jobs install `upsctl` with `go install`, because the `upsctl` image has no shell. Pin a release tag
  instead of `@latest`.

# HTTPS with Caddy

The optional `https` compose profile puts Caddy in front of Upsera on ports 80 and 443.
Caddy gets and renews certificates automatically.

## Set it up

Point your domain's DNS at the server. Then set these in `deploy/.env`:

```sh
UPSERA_DOMAIN=status.example.com
BASE_URL=https://status.example.com
TRUSTED_PROXIES=172.29.0.0/24     # the compose network Caddy runs in
```

Start the stack with the profile:

```sh
docker compose -f deploy/docker-compose.yml --profile local-db --profile https up -d --build
```

Leave out `--profile local-db` when you use an external database.

Without `UPSERA_DOMAIN` the profile serves `localhost` with Caddy's local CA. That is handy to try
it, but browsers will not trust the certificate.

Certificates live in the `caddy_data` volume. See [backups](./backups).

## What the Caddyfile does

`deploy/Caddyfile` does three things:

- It serves the dashboard on `UPSERA_DOMAIN` and proxies to the server.
- For any other hostname it requests a certificate on demand, but only after asking Upsera.
  Caddy calls `/api/tls-allowed` over the internal network. Upsera says yes only for the custom
  domain of a published status page.
- It answers 404 to `/api/tls-allowed` from the public side, so nobody can list your custom domains.

## Trusted proxies

Upsera believes `X-Forwarded-For` only from addresses in `TRUSTED_PROXIES`. It uses the header to
tell visitors apart for the public sign-up limits and the probe registration limit. Without
`TRUSTED_PROXIES`, every visitor behind a proxy shares one limit.

The compose file gives its network a fixed subnet, `172.29.0.0/24`, so you can name it. If that subnet
clashes with your network, change it in `deploy/docker-compose.yml` and in `TRUSTED_PROXIES`.
Leave `TRUSTED_PROXIES` unset when you do not use a proxy.

The login rate limit ignores `X-Forwarded-For`. It counts failures per connecting address and email.

## Custom domains for status pages

A [status page](./status-pages) can have its own domain, for example `status.customer.com`.

1. An instance admin sets the page's custom domain in the dashboard. Editors and API tokens cannot
   set, change or remove it. The domain is claimed for the whole instance.
2. Point the domain's DNS at the server.
3. Publish the page.

The `https` profile then requests a certificate when the first visitor arrives. If you use another
proxy, it must terminate TLS, forward the original `Host` header and be listed in `TRUSTED_PROXIES`.
With Caddy, `on_demand_tls { ask http://upsera:3080/api/tls-allowed }` limits certificates to the domains of
published pages.

On a custom domain only the page, its RSS feed and the subscribe, confirm and unsubscribe links
exist. Every other path returns 404. Subscriber emails link to `https://<domain>/...` for such pages,
so the domain needs TLS to work.

# Security policy

## Supported versions

Upsera is pre-1.0. Security fixes go into the latest `0.x` release only; upgrade to it before
reporting, if you can.

| Version | Supported |
|---|---|
| latest `0.x` release | yes |
| older releases, unreleased commits | no |

## Reporting a vulnerability

Please do not open a public issue, discussion or pull request for a security problem.

Report it privately through GitHub's private vulnerability reporting: open
<https://github.com/davidsugianto/upsera/security/advisories/new> (the repository's **Security** tab,
then **Report a vulnerability**).

Include what you can of:

- the affected version (`upsera version`) and how it is deployed (Docker Compose, binary, behind a
  proxy);
- what an attacker can do, and what access they need first (anonymous, team member, team admin,
  instance admin, a probe);
- steps or a minimal proof of concept to reproduce it;
- any fix or mitigation you suggest.

Upsera is maintained in spare time, so responses are best effort and there is no guaranteed response
or fix time. You will get an acknowledgement when the report has been read, and we will agree on
disclosure timing with you; you are credited in the advisory unless you prefer not to be.

## Scope notes

Upsera is self-hosted: the operator runs the server, its database and its network, so the
deployment is part of the picture.

- **`APP_SECRET`** encrypts notification channel secrets, Docker host TLS material and SMTP settings.
  Anyone who has it and a copy of the database can read those secrets. Keep it out of version
  control and back it up separately from the database. A leaked secret on your own host is a
  deployment issue, not a vulnerability in Upsera; a way to extract it through Upsera is in scope.
- **Docker socket proxy.** The bundled compose file gives the server Docker access only through
  `tecnativa/docker-socket-proxy` with read-only container endpoints. Mounting the raw Docker socket
  into the server, or widening the proxy's permissions, is outside the supported setup.
- **Outbound requests** from checks and notifiers go through a target policy (`internal/netpolicy`).
  A way to make the server reach an address that policy should refuse (for example a cloud metadata
  endpoint) is in scope.
- In scope as well: authentication, sessions and CSRF, API token scoping, cross-team access, public
  status pages and subscriber links, and the probe registration and result protocol.
- Out of scope: denial of service by an authenticated instance admin, findings that need control of
  the host or database, and problems in third-party dependencies with no demonstrated effect on
  Upsera (report those upstream).

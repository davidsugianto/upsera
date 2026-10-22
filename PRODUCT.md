# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users
Small engineering and ops teams who self-host their own monitoring. [Inferred from the README, not interview-confirmed] The daily user watches the monitor list for red or amber, opens a monitor to see why, acknowledges an alert, and sometimes edits a monitor, channel or escalation policy. A second audience, the customers of those teams, reads the public status page, usually during an incident and often on a phone. A third audience reads the docs site to install and configure it.

## Product Purpose
Self-hosted uptime monitoring and status pages for teams. Multi-region checks, fewer false alarms. HTTP, keyword, TCP, ping, DNS, push and Docker checks; alerting with escalation, acknowledgements, dependencies and flap damping; public status pages with incidents and subscribers; DEGRADED state, SLOs with error budgets and a monthly SLA report; remote probes; `upsctl` for monitors as code. Success is a team that trusts the red state enough to act on it.

## Positioning
Self-hosted, one binary, with the dashboard embedded in the server. Quorum across probe regions and flap damping to cut false alarms, and an explicit DEGRADED state (up but slow) between up and down.

## Operating Context
The dashboard is embedded in the Go server binary and served with a strict Content-Security-Policy (`default-src 'self'`): no CDN fonts, scripts or styles, and no inline scripts. Everything must ship inside the binary. Live updates arrive over Server-Sent Events. The public status page is server-rendered Go HTML with an embedded stylesheet, can be served on a customer's custom domain, and must work without JavaScript. The docs site is VitePress, published to GitHub Pages. Tailwind v4, React 19 and uPlot in the dashboard.

## Capabilities and Constraints
- Status vocabulary is fixed: up, down, pending, degraded, maintenance. Status must never rely on colour alone.
- Roles gate actions (viewer, editor, owner, admin); layouts must tolerate hidden actions.
- Print styles for the monthly report must keep working.
- Dashboard text and behaviour stay as they are unless the user approves a change: tests find elements by text and role.
- Open: whether the logo mark changes. Open: fonts on the public status page (recommended: system stack, because custom domains cannot fetch fonts from the dashboard origin).

## Brand Commitments
- **Brand colour is pink** (currently `#C2185B`). The user asked to keep it. It is the signature colour of the product and stays recognisable in light and dark. Pink is for brand and primary actions; status colours (up/down/pending/degraded/maintenance) stay separate and are never pink.
- The name is Upsera. The mark today is a pink dot beside the wordmark.

## Evidence on Hand
Nine real screenshots in `site/public/screenshots/` taken from a seeded demo stack. No customer logos, testimonials or benchmarks exist; none may be invented.

## Product Principles
1. Status first: the state of every monitor is readable in a glance, in both themes, without colour alone.
2. Calm until it matters: red and amber are rare and must stand out because everything else is quiet.
3. Self-hosted honesty: nothing loads from outside the server; the product never pretends to be a hosted service.
4. Fewer false alarms is a product claim, so interfaces show why a state changed (regions, quorum, flaps), not just that it did.

## Accessibility & Inclusion
Aim for WCAG AA contrast in both themes, visible keyboard focus, no colour-only status, reduced-motion respected, and usable at 390px width (status page readers are often on phones during an incident).

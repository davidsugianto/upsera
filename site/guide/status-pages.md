# Status pages

A status page is a public page that shows the state of chosen monitors. It lives at
`/status/<slug>` on your server, and can also be served on its own domain.

![A public status page with a "Degraded performance" banner, two sections of monitors and 90 day bars.](/screenshots/status-page.png)

## Build a page

In the dashboard, create a page with a slug, a title and a description. Add sections, and in each
section add monitors. Each item has a display name. It defaults to the monitor's name, so set a
public name if the monitor name is internal. The page is private until you publish it.

Only this is public: the page title and description, section names, display names, the current state
and incident titles and updates. Heartbeat messages and check targets never appear.

Public pages are server-rendered HTML without JavaScript. The server caches a page for 30 seconds, and
the status banner uses live state.

## Incidents

An incident has a title, a status and a list of updates.

- You can write one by hand.
- Upsera drafts one automatically when a monitor on a published page has had an open alert for
  `INCIDENT_DRAFT_AFTER` (default 5 minutes). A draft is private until you publish it.
- Publishing queues notices for confirmed subscribers.
- An auto-drafted incident resolves when its alert recovers.

## Subscribers

Visitors can follow a page in three ways:

- **Email.** The visitor enters an address and confirms through a signed link (double opt-in). The
  confirmation page needs a button press, so link scanners do not confirm by accident. Sign-ups are
  rate limited, and mail needs the instance SMTP server (Administration, settings).
- **Webhook.** An editor adds a webhook URL. URLs are masked in the API.
- **RSS.** The feed is at `/status/<slug>/feed.xml` and is public.

Delivery retries failed sends with a growing delay and drops notices that stay pending for 24 hours.

## Custom domains

An instance admin can set a custom domain per page. See [HTTPS](./https#custom-domains-for-status-pages).

## Manage pages as code

[upsctl](./upsctl) exports and applies status pages, matched by slug. It cannot set `custom_domain`.

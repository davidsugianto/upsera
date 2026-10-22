---
version: 1
slug: "web-src"
primary_target: "web/src"
related_targets: []
---

# Dashboard surface brief

Scope: the signed-in dashboard (`web/`), plus the login/setup screens. Mode: Operate. Public status page and docs inherit the same world in their own mode (Read).

Audience and job: ops and engineering teams scanning for red or amber, opening a monitor to see why, acknowledging alerts, editing monitors, channels and policies. Usually a laptop in a lit office, sometimes a phone during an incident. Frequent, repeated, task-first use.

Constraints: strict CSP (fonts vendored, no inline scripts); status vocabulary up/down/pending/degraded/maintenance; text and ARIA unchanged so tests keep passing; print styles for reports; brand colour is pink and stays.

Unresolved: logo mark (default: keep the pink dot, refine proportions only); status page fonts (default: system stack).

## Direction contract

THESIS: A two-ink print-shop proof sheet. Calm cool paper, one fluorescent-pink ink for brand and the primary action, one cobalt ink for links/maintenance/selection. It refuses the grey-screen-plus-one-accent SaaS default and the neon-on-black ops dashboard.

OWN-WORLD: Cool newsprint ground (light) and ink-black ground (dark), hairline module borders at 1px, tabular figures in aligned columns, Schibsted Grotesk for UI and a clean technical mono for numerics and targets. Status inks stay separate from brand pink: green up, red down, amber pending/degraded, cobalt maintenance. Pink is solid ink only on the active nav item, primary buttons and the logo. Recognisable with content removed by its pink flood spots, hairline grid and halftone fills.

STORY: Within a second the visitor reads fleet health from the monitor list; red and amber are the only loud things on screen. They trust the state because every mark also has a word or pattern. They act through plain, standard controls.

FIRST VIEWPORT: Left rail (240px): logo, team switcher, nav; the active item is a solid pink flood. Top: page title with a status tally (counts per state as mark plus number). Under it a filter row (search, tag chips). Then grouped monitor modules, each a hairline-ruled table: state mark, name and target, 50-tick heartbeat strip, 24h, 30d, latency in tabular figures. Signature move: states print as halftone dots, so a degraded tick is amber with a dot screen, an exhausted budget bar is red with a dot screen, maintenance is cobalt with a diagonal hatch; colour is never the only signal. Primary action sits top right.

FORM: Candidate 3 of my list (Riso two-ink print studio), assigned by the roll. seed: 86661da46a96869a

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

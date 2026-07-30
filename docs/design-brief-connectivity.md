# fuji-cull — design brief addendum: connectivity

Read the main brief (`design-brief.md`) first — this extends it. Same product,
same identity, same four surfaces (web, desktop, Android, iPad), same tokens.
This addendum covers **two new connectivity features** and asks for the UI they
need: the flows to **connect/configure** them, and the indicators that show
their **status once connected**. Nothing here changes the culling grammar; it
adds a thin layer of "where is my camera / where do my decisions live" on top.

## The two features — and why people confuse them

They sound similar and both live in Settings, so the design's first job is to
make them feel like **two clearly different things**:

- **Remote camera host** — *where the camera is.* Normally the camera plugs into
  the device you're holding. With this on, the camera plugs into **another
  machine** (a desktop or an always-on box on the same network) that runs the
  engine, and your iPad/phone/browser becomes a **thin window** onto it. You
  pick a "camera source": *this device* or *a remote host (URL + key)*. One
  engine, many viewers — everyone sees the identical live card.

- **Cross-device sync** — *where your keep/reject decisions live.* Independent
  devices (each with their own copy) reconcile their decisions through a small
  self-hosted **sync server** so you can start on one and resume on another.
  It's a background service, not a place you "go".

A one-line mental model to design around: **host = the camera; sync = the
decisions.** A user can run either, both, or neither. (When you're a thin client
of a host, you're already sharing that host's decisions, so sync is redundant
*for that link* — but the two still coexist in the UI.)

## Current state (functional, plain — your starting point)

Both already work; they just look like raw settings fields and terse status
strings. What exists today:

- **Config** is two text-field pairs in Settings: *Camera source* (server URL +
  engine key) and *Cross-device sync* (server URL + api key). Saving reconnects.
- **Host status** is a single status line on the Connect screen, e.g.
  `connecting to studio-mac…`, `connected · studio-mac`, `studio-mac: indexing
  the card…`, `wrong key for studio-mac`, `reconnecting to studio-mac…`. There's
  a Settings "Link" row that reads `remote host`.
- **Sync status** is exposed by the engine as `/api/status.sync =
  { enabled, pending, lastOkMs, error, epoch }` plus a per-decision `contested`
  flag. The **web** UI shows a tiny header badge — `SYNCED` / `SYNC ↑N` /
  `SYNC ⚠` — but iPad and Android show **nothing** yet.

So: the plumbing and every state exist; the design work is turning them into
something glanceable and reassuring.

## Where this lives — the header is the stage

The grid header already carries: the **camera device chip** (`X-H2S · …3E21`),
the **K / R / U** counters + thumbnail/EXIF sweep progress, the **CAMERA SICK**
warning, and **IMPORT**. Connectivity status wants to live up here too, next to
the device chip, without crowding it. Part of the ask is a **header layout
strategy** for when several of these are present at once (device + sync + host +
counts + import). Assume this is prime, scarce real estate seen at a glance.

---

## Feature A — Remote camera host

### A1. Connecting / configuring

Design a **"Camera source"** control that reads as a real choice, not two loose
fields:

- A pick between **This device** and **Remote host** (segmented control, two
  cards, or a menu — your call). Selecting *Remote host* reveals **server URL**
  and **engine key** fields.
- A **Test / Connect** affordance that gives immediate, honest feedback:
  reachable + key-accepted (green), reachable-but-wrong-key (red, "check the
  key"), unreachable (amber, "can't reach that address"). Live, not a guess.
- Copy that sets expectations in one line: *"browse a camera plugged into
  another machine on your network."* Assume LAN; no accounts.
- Forward-looking (design the slot, mark it optional/coming): **discovery** —
  "Found **studio-mac** on your network — connect?" so the common case needs no
  typing.

### A2. The connect experience (its own flow, distinct from camera bring-up)

The main brief's Connect screen is a **camera-over-USB bring-up** (a ~3-minute
indexing wait). A remote host is a **different feeling** — it's a network
handshake, usually seconds, sometimes a reconnect. Give remote connect its own
treatment so the user knows which world they're in. States to express:

- **connecting** to `<host>` (brief; a networked-handshake feel, not a USB one)
- **connected** — hand off to the grid
- **host is indexing the card** — the host, not you, is doing the ~3-min index;
  show it as *the host's* progress, at a calm distance ("studio-mac is reading
  the card…")
- **wrong key** — clear, correctable, points back to settings
- **reconnecting** — the host slept / WiFi blipped; **do not** slam back to a
  cold connect screen. This should feel like a momentary reconnect *over* the
  grid where possible (see A3), not a teardown.

### A3. Status once connected

A persistent, glanceable **host chip** in the grid header — the "you are looking
at a camera on another machine" reminder — with a connection state:

- **connected** (calm, e.g. a steady dot + `studio-mac`)
- **reconnecting** (amber, non-alarming — a pulse, not a modal; the grid stays)
- **lost** (only after it's really gone — a quiet banner offering retry/settings)

In *this device* mode this chip is absent (the existing camera device chip does
the job). Design how the host chip and the camera device chip **relate** — one
replaces the other by mode, ideally sharing a shape so the header doesn't jump.
A brief reconnect must be a **soft, in-place** indication, never a full-screen
bounce — this is a real pain point today.

---

## Feature B — Cross-device sync

### B1. Configuring

A **Cross-device sync** section: an **enable** affordance + **server URL** and
**api key**, with a one-line explainer *("sync keep/reject across your devices
through your own server")* and, like the host, a **reachability check**. A
disabled state should look deliberately off, not broken.

### B2. Status once connected — a compact header indicator with real states

Turn `/api/status.sync` into a single glanceable chip (the web's `SYNCED /
SYNC ↑N / SYNC ⚠` is the seed — make it first-class on iPad/Android too):

- **synced** — everything's up (quiet check; the resting state, shouldn't shout)
- **syncing / N pending** — `pending` decisions still to upload (a subtle up-count)
- **offline** — can't reach the sync server right now (queued locally, will
  retry — reassure, don't alarm; nothing is lost)
- **error** — a real problem (e.g. wrong key); actionable, routes to settings
- **disabled** — absent or a faint "off" (don't imply a failure)

Tapping it should reveal a small **detail popover**: last-synced time, pending
count, per-device presence ("iPad, MacBook"), and any contested items.

### B3. Contested decisions (the one richer element)

When two devices decided the **same photo differently** while offline, the
server flags it `contested`. This must be **surfaced, never silently resolved**.
Design:

- a **per-shot mark** on the tile (a small, distinct-from-keep/reject glyph —
  "these disagree") that stays glanceable in the grid, and
- a lightweight **resolve** affordance in the viewer or the sync popover:
  *"two devices disagreed — keep or reject?"* — a one-tap decision, not a wall.

Keep it rare-by-nature and calm; it's a nudge, not an error.

### B4. Resume-across-devices (forward-looking, design the slot)

Sync also carries a per-device **resume point**. The intended UX is an
**advisory** chip — *"Resume where iPad left off — DSCF0071, 2m ago"* — that a
user can **tap to jump to**, but which **never** yanks their current scroll.
Design this as an opt-in suggestion (a dismissible pill near the header or on
first open of a camera), not an automatic jump. Mark it as a later phase.

---

## States to express (checklist)

**Host:** this-device · connecting · connected(`host`) · host-indexing(progress)
· wrong-key · reconnecting · lost.

**Sync:** disabled · synced · syncing(`N` pending) · offline · error · contested
(count) · resume-available(`host`, `shot`, `age`).

**Reachability check (both):** untested · checking · reachable+authed(ok) ·
reachable+wrong-key · unreachable.

## Constraints (in addition to the main brief's)

- **Glanceable, peripheral, calm.** These are *ambient* states a user checks in
  a half-second between decisions. The resting states (synced, connected) should
  be the quietest; only real problems (wrong key, lost, contested) earn
  attention. Never a spinner-modal that blocks culling.
- **Reconnect is not disconnect.** A momentary network blip must read as a soft,
  in-place "reconnecting", not a teardown to a cold screen. (This is the single
  biggest current wart.)
- **Honest, like the rest of the app.** Real words and real counts ("3 pending",
  "last synced 2m ago", "can't reach studio-mac"), never a fake OK.
- **Reassure on offline.** Both features are offline-tolerant by design — queued
  and retried. Offline states must communicate *"safe, will catch up"*, not loss.
- **Header economy.** The device/sync/host/counts/import chips may all be
  present; give a coherent priority + layout so the header degrades gracefully
  on a phone width and never wraps chaotically.
- **Reuse the grammar.** Lean on existing tokens: `amber` for the active/primary
  and focus, `keep`/`reject` reserved for decisions (find *different* hues for
  sync/host state so they don't read as decisions — `buffered` blue `#4EA6FF`
  and `immich` teal `#57C9C1` are unclaimed neighbors worth considering), `text2`
  /`text3` for the resting/muted states, the `surface`/`line` chip shapes.

## What we want from you

1. The **Camera-source picker** (this-device / remote-host) and the
   **Cross-device-sync section** as polished Settings components — with the
   reachability-check states.
2. The **remote-connect flow** for the host (distinct from USB bring-up),
   including the *reconnecting-over-the-grid* soft state.
3. The **header connectivity chips** — host chip + sync chip — with all states
   above, a **tap-to-detail popover** for sync, and a **header layout/priority**
   strategy for when everything's present (down to phone width).
4. The **contested** per-shot mark + one-tap resolve, and the **resume-across-
   devices** advisory pill (mark it as a later phase).
5. Platform notes where idioms differ (iPad sheet vs Android dialog vs web
   inline), but keep one shared grammar.

Deliver as annotated mockups (settings, connect, header-with-chips, popover,
contested tile + resolve) plus the added component inventory, in the existing
dark identity. Where you propose new hues/shapes, show them against the current
header so we can see it doesn't fight the tile grammar.

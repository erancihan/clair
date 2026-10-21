> **Status (2026-09-02): built** (roadmap v1 steps 7–8, plan `plans/2026-08-06-live-boards-and-screens.md`). The "before" measurements in the tables below describe the state this design replaced. Known gaps found on audit are tracked in `2026-09-02-evaluation-and-roadmap-v2.md` §2.2–2.3 (stream fallback, GA showing stream, window mismatch).

# Live boards and per-service screens — design

Three complaints, one underlying rule.

1. The theater board wastes its width on a two-column grid of tall, nearly empty
   cards.
2. Each film shows one or two times per day, so days should be tabs rather than
   headings stacked down the page.
3. Subscriptions are the wrong shape: a page should open **one** connection and
   subscribe to **only what it displays**.

The hospital, consulate and bureau pages have the same structural faults the
theater had before its rework, so they are in scope here too.

## The rule

**One screen, one subscription, covering exactly what that screen shows.**

This is not tidiness. HTTP/1.1 caps a browser at roughly six connections per
origin, and an open EventSource holds one for its entire life. A page that
subscribed per showing would spend the tab's whole connection budget on itself
and then stall its own CSS and images. The ceiling is the constraint; the
tidiness is a side effect.

Where things stand today, measured rather than assumed:

| Screen | Requests | Streams |
|---|---|---|
| Theater board | 13 — one per showing | **0**; counts are never live |
| Seat screen | 2 (event, seats) | 1 — correct |
| Hospital / consulate / bureau | 1 per expanded card | 0 |

So the board is not over-subscribed, it is *un*-subscribed and over-fetched: 13
requests collapse to 1, and 1 stream appears where there were none.

## Surfaces

### A. Theater board — full width, one date strip

A single date strip across the top, one pill per day that has a showing, the
earliest such day selected. Horizontally scrollable on narrow screens. Choosing a
day filters the whole board; a film with nothing that day drops out rather than
rendering an empty card.

Film rows go full width and stop pretending to be cards in a grid. Within a row,
on wide screens, the meta column is fixed and narrow — title, salon, seating
kind, price — and the showtimes take the remaining width. That is what uses the
width; the current two-column grid produces a tall card holding one chip.

Each row carries a **monogram tile** (the film's initial, muted tint). There are
no posters in this system, and without an anchor a full-width row of mostly empty
space reads as unfinished rather than spare.

Showtimes: time large and tabular, "N left" beneath, sold out struck through and
inert. Each remains a link to its own seat screen.

An empty day says so — "No showings on Thursday 6 August" — and offers the next
day that has one.

### B. Seat screen — unchanged

Already one stream for one event. Nothing here changes.

### C. Institution list pages

`/aurenor/hospitals/{slug}`, `/aurenor/consulate`, `/aurenor/bureau` become
lists of services and nothing more. They fetch no availability, because they
display none. This is what makes the subscription rule true rather than
aspirational: there is no screen that shows Cardiology and Pediatrics
availability at once, so there is no screen that has to subscribe to both.

### D. Appointment detail screen — new

`/aurenor/hospitals/{slug}/{service}`, `/aurenor/consulate/{service}`,
`/aurenor/bureau/{service}`.

One service, one screen: date strip, that day's times grouped Morning /
Afternoon, the booking form directly beneath the chosen time, then the
confirmation. One availability request, one stream.

This kills the four blockers the audit found on the appointment pages:

- the form is adjacent to the choice instead of below the whole list;
- the date strip caps the chip count — the bureau's ~170 becomes ~30;
- a 409 keeps its message instead of clearing it on the refresh it triggers;
- a booking produces a visible confirmation, and the cancel-token warning is
  shown **before** booking rather than after it is too late to act on.

## Server work

### 1. `GET /events/summary?slugs=a,b,c`

Returns one row per known slug: `slug`, `name`, `start_at`, `status`,
`max_per_buyer`, and the first tier's `id`, `kind`, `price_cents`, `currency`,
`remaining`. Unknown slugs are omitted; the board reports the shortfall the way
it already reports failed loads.

`slugs` is capped at **50** per request. An uncapped list is an invitation to
make one request do arbitrary work.

### 2. `GET /events/summary/stream?slugs=a,b,c`

The same array, pushed whenever any of those events changes. The handler
subscribes to every named event topic through one hub registration, coalesces the
burst, and sends one payload. Server-side this is still a single Postgres
listener; browser-side it is a single connection.

This needs one hub addition: `SubscribeAll(topics []string)`, registering one
channel against many topics. Subscribing N times and selecting over N channels
would otherwise require `reflect.Select` or a fan-in goroutine per connection.

### 3. `GET /appointments/schedules/{slug}/availability/stream`

Pushes `[]OpenSlot` for the schedule's window whenever its slots change. Topic
`schedule:<id>`, which the existing trigger already emits: `appointments.Book`
calls `BookDirect`, an inventory UPDATE, and cancellation decrements the same
row.

The stream always sends the schedule's own bookable window, capped at 14 days,
computed server-side. The client filters to the selected day. Fixing the window
server-side keeps it to one cache entry per schedule instead of one per client's
choice of range.

### 4. Availability caching

Appointment availability is *computed* from rules, not stored, so it is more
expensive to produce than a seat map and equally worth sharing. It gets the same
treatment: one rendered snapshot per schedule per change, versioned by the
`schedule:<id>` counter, shared by every reader.

The existing `seatCache` becomes a small generic snapshot cache
(`snapshotCache[T]`) used three times — seats, event summaries, availability —
rather than three near-identical caches.

### 5. Triggers on rules and exceptions

Today only inventory changes announce themselves. A host editing opening hours or
adding a closure changes availability without touching inventory, so an open
screen would not hear about it. Add the same `pg_notify('booking_inventory',
'schedule:<id>')` on `booking_availability_rules` and
`booking_availability_exceptions` (INSERT, UPDATE, DELETE).

## Frontend work

- **One date-strip component**, used by the board and the appointment screen. The
  two surfaces must not drift into two different date pickers.
- **Board component** rewritten: one summary fetch, one stream, day filtering,
  full-width rows.
- **Appointment detail component**: availability fetch, stream, day filtering,
  booking, cancel-token handling, confirmation.
- **List pages** trimmed to links.
- Polling remains the fallback on both, for a browser or proxy that will not
  carry a stream.

## Testing

- **Cache**: concurrent readers coalesce to one load; one version bump costs one
  load; entries do not bleed across keys; ETag tracks content. (Extends the
  existing seat-cache tests to the generic type.)
- **Summary endpoint**: shape, unknown slugs omitted, slug cap enforced.
- **Summary stream**: a hold on one of the named events pushes an updated array
  to a second connection.
- **Availability stream**: a booking pushes an updated slot list; a rule change
  does too (this is what proves the new triggers).
- **Routes**: the new appointment detail routes resolve, and 404 on an unknown
  service or venue.
- **Render**: board has no two-column grid and does carry a date strip; list
  pages carry links rather than availability components.
- **Alpine logic** in node, as before: day grouping, default day selection,
  empty-day handling.

## Limits and risks

- Availability recompute is heavier than a seat read. The cache makes it once per
  change rather than once per reader, but a schedule under constant change will
  recompute constantly. The window cap (14 days) bounds each recompute.
- 50 slugs per summary request is a guess at "more than any real board, less than
  a denial of service". It is a constant, easy to revisit.
- The appointment stream sends the whole window on every change, not a delta.
  With a 14-day cap this is tens of kilobytes; deltas are a later optimisation if
  the payload becomes the problem.

## Out of scope

Posters. Per-day fetching. Moving the games and auth route registration into
`internal/server/routes` (worth doing, unrelated to this).

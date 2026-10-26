# Aurenor

The made-up nation the booking examples are themed around. Completely fictional, with no real-world names. A coastline runs along its north, the Gilt Coast. The setting is reserved to grow: the booking demos today, a map and a game later.

## The nation

| | |
|---|---|
| Name | Aurenor |
| Meaning | "golden north". Elegant, faintly mythic. |
| Demonym | Aurenori |
| Capital | Solvarre |
| North coast | the Gilt Coast |
| URL namespace | `/aurenor` (country home, reserved for a future map or game) |

## Institutions

Two booking systems, several example institutions on top. Theaters and hospitals are collections: each lists instances with their own `{slug}`, so the nation can hold more than one of each. The consulate and the civic bureau are single bodies with flat pages.

| Institution | System | Route |
|---|---|---|
| Theaters (Aurenor Grand Theater, Harborlight Cinema) | seat reservation | `/aurenor/theaters`, `/aurenor/theaters/{slug}` |
| Hospitals (Solvarre General Hospital, Gilt Coast Clinic) | appointment scheduling | `/aurenor/hospitals`, `/aurenor/hospitals/{slug}` |
| Aurenor Consulate | appointment scheduling (visa interviews, residence permits) | `/aurenor/consulate` |
| Aurenor Civic Bureau | appointment scheduling (licence, ID, fingerprint) | `/aurenor/bureau` |

Every institution runs on the same booking service. How it works is in [booking-service.md](booking-service.md). What is built and what comes next is in [roadmap.md](roadmap.md).

## Alternative names considered

Kept for future settings, sub-regions, or a rename. Each was drafted with its own north coast.

| Name | Vibe | Demonym | Capital | North coast |
|---|---|---|---|---|
| Norrevik | "north bay". Nordic, grounded, believable. | Norrevikan | Halden's Reach | the Norre Sea |
| Kestrand | "shore land". Coastal, leans hardest into the north-coast premise. | Kestrandi | Marisk | the Kest strand |
| Sundmark | "strait borderland". Nautical, a maritime nation. | Sundmarker | Vantholm | the Cold Sund |

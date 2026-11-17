# RecallRoute

**Recall-to-shelf matching for food banks and small grocers.** RecallRoute reads FDA food recall notices, works out which lots on *your* shelves they cover (by barcode, lot code and best-by date), and walks volunteers through pulling the product and telling the partner pantries that received it, with live updates and an audit trail.

[![CI](https://github.com/yashchhabra57/recallroute/actions/workflows/ci.yml/badge.svg)](https://github.com/yashchhabra57/recallroute/actions/workflows/ci.yml)
![Go](https://img.shields.io/badge/Go-1.25-00ADD8)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-16-336791)

![Alert board](docs/board.png)

## The problem

The FDA posts dozens of food recalls every week. A regional food bank holds thousands of lots of donated and purchased food, then ships them on to dozens of partner pantries and shelters. Today someone reads each recall notice by hand, searches a spreadsheet for the product, checks lot codes one by one, and phones the partners who might have received it.

Recall notices are hard to match because the important detail is written as free text:

> `Lot codes 2341A through 2349A; Best By 12/01/2026 - 12/15/2026`
> `Northwoods Creamery Cheddar Cheese Curds, 12 oz bag, UPC 0 71234 00101 9`
> `No codes` · `Illinois: TFIC211, 8/7/2026. Texas: TFTX209U11, 8/7/2026` · `Best By JUL 22 27`

A missed match means a recalled product goes home in a family's grocery bag. RecallRoute turns that text into something a computer can check, flags the matching lots, and tracks each one until it is off the shelf and everyone downstream has been told.

## What it does

- **Ingests recalls** from the openFDA food enforcement API (polled on a schedule with conditional GETs) or an uploaded JSON file.
- **Imports inventory** from CSV exports (header aliases like `UPC`, `Lot Number`, `Exp`, `Qty`; bad rows are reported, not fatal).
- **Matches recalls to lots** in two ways:
  - *Barcode path*: GTINs pulled from the notice text are looked up in an index, then the lot code and best-by date are checked against the parsed code info.
  - *Name path*: for stock with no barcode on file (common with donations), product-name tokens, brand and package size are compared. These matches are only ever suggestions for a person to confirm.
- **Grades every match**: `confirmed` (barcode + lot/date), `probable` (barcode, but the lot can't be checked), `review` (name similarity only).
- **Runs the pull-and-notify workflow**: `new → acknowledged → pulled → notified → closed` (or `dismissed` with a reason). Pulling takes units off the shelf count; notifying records which partner agencies were told. Every step is an audit event.
- **Pushes live updates** over WebSocket so every volunteer's board changes the moment an alert is created or moved.
- **Prevents lost updates**: each alert has a version number. Two people acting on the same alert at once get one success and one `409 Conflict`, never a silent overwrite.

## Architecture

```mermaid
flowchart LR
    FDA[(openFDA food<br/>enforcement API)] -- "poll: ETag / 304,<br/>backoff on errors" --> P[poller]
    CSV[/inventory CSV/] --> API
    UI[Board UI<br/>vanilla JS] -- REST --> API[api<br/>chi router]
    UI <-- WebSocket --> HUB[hub<br/>broadcast]
    P --> SVC
    API --> SVC[service]
    SVC --> FEED[feed<br/>openFDA parser]
    FEED --> GTIN[gtin<br/>barcode extract + check digit]
    FEED --> LOT[lotcode<br/>code-info parser]
    SVC --> M[match<br/>GTIN index + token index]
    SVC --> WF[workflow<br/>state machine]
    SVC --> ST{{store interface}}
    ST --> PG[(PostgreSQL<br/>pgx, migrations)]
    ST --> MEM[(in-memory)]
    SVC -- events --> HUB
```

| Package | Responsibility |
|---|---|
| `internal/gtin` | Normalize UPC-A / EAN-8 / EAN-13 / GTIN-14 to 14 digits, verify the GS1 check digit, extract codes from text (including 11-digit UPCs printed without a check digit). |
| `internal/lotcode` | Parse code info into a `Spec`: exact lots, numeric ranges (`24A100-24A150`), prefixes, "all lots", "no codes", and best-by date windows in six date formats. |
| `internal/feed` | Turn openFDA enforcement JSON into `domain.Recall`. |
| `internal/match` | Build an inventory index and produce graded match candidates. |
| `internal/workflow` | Pure state machine: validates a command and returns the new alert, an audit event and the quantity to quarantine. |
| `internal/store` | `Store` interface plus `memstore` and `pgstore`; one contract test suite runs against both. |
| `internal/service` | Match on ingest and on import; apply transitions; publish events. |
| `internal/hub` | WebSocket fan-out; slow clients are dropped instead of blocking everyone. |
| `internal/poller` | Scheduled feed fetch with `If-None-Match`, `If-Modified-Since` and jittered exponential backoff. |
| `internal/api` | HTTP handlers, error mapping, bearer-token auth for writes. |
| `web` | Embedded board UI (no build step) and demo data. |

## Tech stack

Go 1.25 · chi v5 · pgx v5 + PostgreSQL 16 (embedded SQL migrations, advisory lock) · coder/websocket · `log/slog` structured logs · vanilla JS + CSS (embedded with `go:embed`) · GitHub Actions · Docker (distroless, non-root).

## Run it

**Quick demo (no database):**

```bash
go run ./cmd/recallroute
```

Open http://localhost:8080, click **Load demo inventory** then **Load demo recalls**. Five alerts appear: three confirmed, one probable (a lot with no code on file) and one review (oats with no barcode).

**With PostgreSQL:**

```bash
docker compose up --build
```

or point at any Postgres:

```bash
DATABASE_URL=postgres://user:pass@localhost:5432/recallroute?sslmode=disable go run ./cmd/recallroute
```

**Poll the real FDA feed:**

```bash
RECALLROUTE_FEED_URL='https://api.fda.gov/food/enforcement.json?search=report_date:[20261101+TO+20261231]&limit=100' go run ./cmd/recallroute
```

All settings are in [`.env.example`](.env.example).

### API

| Method | Path | Body | Notes |
|---|---|---|---|
| `GET` | `/healthz` | | store kind, auth on/off, polling on/off |
| `GET` | `/api/recalls` | | newest first |
| `POST` | `/api/recalls/ingest` | openFDA JSON | returns `{received, changed, new_alerts}`; re-ingesting an unchanged feed is a no-op |
| `POST` | `/api/recalls/poll` | | only when a feed URL is configured |
| `GET` | `/api/lots` | | |
| `POST` | `/api/lots` | `text/csv` or JSON array | returns `{imported, new_alerts, row_errors}` |
| `GET` | `/api/alerts?state=new` | | with recall + lot |
| `GET` | `/api/alerts/{id}` | | with history and allowed actions |
| `POST` | `/api/alerts/{id}/transitions` | `{"version":2,"action":"pull","actor":"sam","quantity":12}` | `409 version_conflict`, `409 invalid_transition`, `422 validation` |
| `GET` | `/ws` | | WebSocket: `alert.created`, `alert.updated`, `recalls.ingested`, `lots.imported` |

Writes need `Authorization: Bearer $RECALLROUTE_API_TOKEN` when a token is set.

### Tests

```bash
go test ./...                                   # 68 tests, no services needed
go test -race ./...                             # same with the race detector (needs cgo)
RECALLROUTE_TEST_DATABASE_URL=postgres://...  go test ./internal/store/pgstore/   # contract suite on Postgres
go run ./scripts/smoke -base http://localhost:8080                                # end-to-end against a running server
go test -run '^$' -bench . ./internal/match/    # matcher benchmark
```

CI runs: gofmt / vet / `go mod tidy` drift / JS syntax; race tests on Go 1.25 and latest stable; the store contract suite and the smoke script against a PostgreSQL 16 service container; and a Docker build that is started and smoke-tested.

## Engineering notes

- **Safety-biased matching.** When a notice gives both lot codes and best-by dates, a lot that matches *either* is flagged. When the notice can't be parsed, or the lot record lacks the field the notice depends on, the result is "unknown", which becomes a `probable` alert rather than silence. A false alarm costs a volunteer five minutes; a miss can make someone sick.
- **Tested against real notices.** The first parser version understood the code info of 50 of 99 live openFDA reports (Sept-Nov 2026). The misses were mostly "No codes" (meaning every unit is recalled), unlabelled code lists and dates like `JUL 22 27`. After fixing those, coverage on the same sample is 97 of 99; the strings that broke it are now unit tests. Unlabelled text is read as a code list only when at least 30% of its words look like lot codes, so prose is not mined for numbers.
- **Range vs. hyphenated code.** `24A100-24A150` is a range, but `24-123` is one lot code. A bare hyphen counts as a range only when both sides share a prefix/suffix and have the same number of digits; `through`/`to` are always ranges.
- **Barcode check digits do double duty.** Bare 12–14 digit runs in a notice count as barcodes only if the GS1 check digit is valid, which filters out most phone and lot numbers. An 11-digit "UPC" (check digit dropped, which recall notices often do) is completed and the match is marked as inferred, which lowers its score.
- **Two indexes, not a nested loop.** Barcode matches are a map lookup. Name matches use an inverted token index so a recall is compared only with lots that share at least one word. A barcoded lot is never name-matched against a recall that lists *other* barcodes, and a size mismatch (12 oz vs 24 oz) rules a lot out. Benchmark: ~4.5 ms per recall against 20,000 lots on a laptop.
- **Matching runs both directions.** New recalls are checked against current stock; newly received lots are checked against every active recall, so stock that arrives after a recall is still caught. Unique `(recall_id, lot_id)` keeps the two paths from creating duplicates.
- **Concurrency.** `Transition` locks the alert and lot rows (`SELECT … FOR UPDATE`), checks the client's version, then updates the alert, decrements the lot and appends the event in one transaction. The contract suite fires 8 concurrent transitions at one alert and requires exactly one winner, on both stores.
- **Workflow as a pure function.** `workflow.Apply` takes an alert, a lot and a command and returns the result; it touches no I/O. That makes every rule (no pulling more than is on the shelf, dismissals need a reason, review-tier matches need a confirming note) a table test.
- **Polling politely.** ETags and Last-Modified make an unchanged feed cost one `304`. Validators are saved only after a successful ingest, so a failed ingest is retried with a full fetch. openFDA returns `404` for "no results", which is treated as an empty week rather than an error.
- **Untrusted text in the UI.** Recall text comes from an outside feed, so the UI builds the page with `textContent` only, never `innerHTML`.

## Limitations

- Single-tenant. There are no user accounts, just one shared write token; the actor name on each action is whatever the volunteer types.
- Notifications are recorded, not sent. The `notify` step logs which partners were told; it does not send email or SMS.
- The name path is lexical (tokens, brand, size). It will miss a match where the inventory says "PB crackers" and the notice says "peanut butter sandwich crackers".
- Two of 99 real notices still fail to parse (`Best if use by: 032327 TA`, `05 16 2027`). Those become `probable` alerts, not misses, but a person has to read them.
- Only the openFDA food enforcement format is supported. USDA FSIS (meat and poultry) recalls use a different feed.
- The WebSocket tells clients *that* something changed and they refetch; it does not stream diffs.

## Roadmap

- USDA FSIS recall feed adapter.
- Track outbound shipments so `notify` can pre-fill exactly which partner agencies received the recalled lots.
- Email/SMS notification outbox with retries (the store already records recipients per event).
- Per-user accounts and roles (warehouse vs. lead).
- An optional LLM pass that turns unparseable code info into a `Spec`, validated against the same parser types and never trusted over a barcode match.
- A barcode-scanner mode in the UI for confirming review-tier matches at the shelf.

## License

MIT. See [LICENSE](LICENSE).

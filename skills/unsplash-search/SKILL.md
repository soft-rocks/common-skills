---
name: unsplash-search
description: Search the Unsplash photo API from the command line and print candidate image URLs with the required attribution. Use when a task needs a stock photo, a cover image, a background, or an illustration.
---

# unsplash-search

Source: `skills/unsplash-search/scripts/main.go`, a Go module with its own `go.mod`.

## Requirements

`UNSPLASH_ACCESS_KEY` must be set. Nothing loads `.env` automatically, so
source it into the environment first:

```bash
set -a; . "$(git rev-parse --show-toplevel)/.env"; set +a
```

Or pass the key per run with `-access-key`. A free key allows 50 requests per
hour, and a 403 means that limit was reached.

| Variable | Default | Purpose |
|---|---|---|
| `UNSPLASH_ACCESS_KEY` | none | Required. API access key. |
| `UNSPLASH_LEDGER_PATH` | `/tmp/common-skills/unsplash-search/used-photos.json` | Where the used-photo ledger lives. `-ledger` overrides it. |

## Usage

Always run through `go run`. Do not `go build` and do not leave a binary behind.

```bash
cd skills/unsplash-search/scripts

go run . -query "mountain sunset" -n 5
```

Queries must be in English. The API returns nothing for other languages. Start
with broad keywords, since a narrow query often returns zero results.

Each result prints the photo id, dimensions, alt text, three image URLs, the
dominant colour, and the attribution line. Diagnostics go to stderr, so `-raw`
pipes cleanly:

```bash
go run . -query "city skyline" -n 3 -raw | head -1
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-query` | empty | Search keywords. Required unless using `-download` or `-mark`. |
| `-access-key` | empty | Access key. Falls back to `$UNSPLASH_ACCESS_KEY`. |
| `-n` | `10` | Results per page, 1 to 30. |
| `-page` | `1` | Page number. |
| `-order` | `relevant` | Sort order, `relevant` or `latest`. |
| `-color` | empty | Colour filter, for example `blue`, `black_and_white`, `green`. |
| `-orientation` | empty | `landscape`, `portrait` or `squarish`. |
| `-content-filter` | `low` | Safety filter, `low` or `high`. |
| `-raw` | false | Print only image URLs, one per line. |
| `-no-attrib` | false | Omit the attribution lines. |
| `-download` | empty | Register a download for a photo id, as the API guidelines require. |
| `-mark` | empty | Record an image URL as used, so later searches skip it. |
| `-ledger` | `$UNSPLASH_LEDGER_PATH` | Ledger path. |
| `-no-dedupe` | false | Ignore the ledger for this search. |
| `-reset-ledger` | false | Clear the ledger and exit. |

## Deduplication

Searches read the ledger and drop photos already recorded there, paging further
to make up the requested count. This keeps the same popular stock photos from
resurfacing across repeated queries on a topic. `skipped N already-used
photo(s)` on stderr is normal output.

The ledger only fills up if you record what you use:

```bash
url=$(go run . -query "mountain sunset" -n 1 -raw 2>/dev/null)
go run . -mark "$url"
```

Entries are keyed on the stable `photo-<id>` path segment of the image URL, so
size and format parameters do not matter. A missing or corrupt ledger is treated
as empty, since this is a convenience rather than a source of truth.

The skip count can exceed the number of ledger entries, because Unsplash itself
repeats a photo across pages at small page sizes.

### Ledger file

`reset_at` records when the ledger was last cleared, and each photo carries the
time it was marked.

```json
{
  "reset_at": "2026-09-02T11:34:34+08:00",
  "photos": {
    "photo-1540979388789-6cee28a1cdc9": "2026-09-02T11:34:34+08:00"
  }
}
```

Nothing clears the ledger on its own. Run `-reset-ledger` when results start
feeling too constrained, or delete the file.

## Attribution

The Unsplash API guidelines require crediting the photographer and registering a
download when an image is actually used. Every result prints both a plain text
and an HTML credit line, and `-download <photo-id>` performs the registration.

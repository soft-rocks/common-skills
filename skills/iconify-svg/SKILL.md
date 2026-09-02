---
name: iconify-svg
description: Search Iconify (https://icon-sets.iconify.design/) and download icons as SVG. Supports cross-set search, listing sets, listing icons in one set, and downloading with a colour and size. Use when a task needs an icon, an SVG symbol, or a matching icon set for a page or slide deck.
---

# iconify-svg

Source: `skills/iconify-svg/scripts/main.go`, a Go module with its own `go.mod`.

The icon-sets.iconify.design site is only a browser for `https://api.iconify.design`.
This skill calls that API directly, with no browser, no API key and no login.

## Usage

Always run through `go run`. Do not `go build` and do not leave a binary behind.

```bash
cd skills/iconify-svg/scripts
```

Keywords are English only. A Chinese or Japanese term returns nothing, so search
for `scale` or `balance` rather than a translated word. Broad single words work
better than phrases: `gavel` returns 32 results where `scale of justice` returns
none.

### search

```bash
go run main.go search -q gavel -limit 40
go run main.go search -q scale -prefix tabler
```

Results are grouped by icon set, each group labelled with its name and licence.

| Flag | Default | Description |
|---|---|---|
| `-q` | required | English keywords. |
| `-prefix` | empty | Restrict to one icon set. |
| `-limit` | `64` | Maximum results. The API caps this at 999. |

### sets

```bash
go run main.go sets
go run main.go sets -q material
```

Prints prefix, name, icon count and licence for every set.

### list

```bash
go run main.go list -prefix mdi -q scale
go run main.go list -prefix lucide -limit 0
```

`-q` is a substring match on the icon name, where `search` is Iconify's own
semantic search. Use `list` when you know the set and want to see what else it
carries, and `search` when you do not know where to look. `-limit 0` prints all.

### get

```bash
go run main.go get mdi:scale-balance tabler:gavel -out ./assets/icons -color "#1f3a5f" -size 48
go run main.go get mdi:home -name house
```

Icons are written as `prefix:name` and several can be passed at once, freely
interleaved with flags. The file name defaults to `<prefix>-<name>.svg` and an
existing file is overwritten.

| Flag | Default | Description |
|---|---|---|
| `-out` | `/tmp/common-skills/iconify-svg` | Output directory, created when missing. Pass a project path to keep the icons. |
| `-name` | empty | Custom file name without `.svg`. Only valid for a single icon. |
| `-color` | empty | Replace `currentColor`, for example `"#1f3a5f"` or `red`. |
| `-size` | `0` | Set width and height together. 0 keeps the original size. |
| `-width` / `-height` | `0` | Set individually, overriding `-size`. |
| `-flip` | empty | `horizontal`, `vertical`, or `horizontal,vertical`. |
| `-rotate` | `0` | Only 90, 180 or 270. |
| `-box` | false | Add a transparent frame equal to the viewBox, for aligning several icons. |

Downloads are spaced 200ms apart. If any icon fails the command exits 1, and the
ones that succeeded are still written.

## Picking icons

1. Search with English keywords, read the grouped output, and choose a few that
   share a style. Do not take everything.
2. Keep icons for one page or one video from a single set, such as `mdi`,
   `tabler`, `lucide` or `ph`, so stroke weight and corner radius match.
3. Set the colour and size at download time rather than editing the SVG after.

`-color` only affects single-colour sets. Sets marked `palette: true`, such as
`fxemoji` and `twemoji`, carry their own colours and ignore it.

## Licensing

Iconify forwards each set's original files, so the licence follows the source
set, usually Apache-2.0, MIT or CC BY 4.0. Both `search` and `sets` print it.
Check that column before publishing anything.

## Limits

- Keywords are English only.
- SVG only. The API has no PNG endpoint, so convert separately if you need a raster image.

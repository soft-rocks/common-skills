---
name: text-to-svg
description: Render text as SVG outline paths from a font's own glyphs, with configurable font, size, fill, stroke, colour, spacing and background. Use when a task needs text as vector artwork rather than a text element, for a logo, a title, a cut file, or a page that must not depend on a webfont.
---

# text-to-svg

Source: `skills/text-to-svg/scripts/main.go`, a Go module with its own `go.mod`.

Reads glyph outlines with `golang.org/x/image/font/sfnt` and writes them as SVG
path data. No browser, no rasterization, and the result is real vector geometry,
so it scales and can be edited in any vector tool.

## Requirements

Nothing. No API key and no account.

Family lookup uses the two endpoints fonts.google.com uses for itself:
`fonts.google.com/metadata/fonts` for the family list, and
`fonts.googleapis.com/css2` for a family's font file URL. Google's developer API
at `googleapis.com/webfonts` does require a key, which is why browser tools such
as google-font-to-svg-path carry one, but it offers nothing extra here.

## Usage

Always run through `go run`. Do not `go build` and do not leave a binary behind.

```bash
cd skills/text-to-svg/scripts

go run . -font-file /path/to/font.ttf -text "Hello" -size 96 -fill "#1f3a5f" \
  -out ./title.svg
```

With a Google family, downloaded once and cached under
`/tmp/common-skills/text-to-svg/fonts`:

```bash
go run . -list serif                     # search families
go run . -list '*'                       # all 1946
go run . -font "Playfair Display" -variant 700 -text "Typography" -size 90
```

`-list` prints the family name, its category, and its available weights. Those
weight names go straight into `-variant`.

`-out -` writes the SVG to stdout instead of a file, so it can be piped.

### Flags

| Flag | Default | Description |
|---|---|---|
| `-text` | `Hello` | Text to render. `\n` separates lines. |
| `-font-file` | empty | Local `.ttf` or `.otf`. Skips the API entirely. |
| `-font` | empty | Google Fonts family name, for example `Noto Serif KR`. |
| `-variant` | `regular` | Font variant. Accepts `regular`, a weight such as `700`, and either italic spelling, `700i` or `700italic`. |
| `-size` | `72` | Font size in SVG units. |
| `-fill` | `#000000` | Fill colour, or `none` for outline only. |
| `-stroke` | empty | Stroke colour. Empty means no stroke. |
| `-stroke-width` | `1` | Stroke width. |
| `-line-height` | `1.2` | Line height as a multiple of size. |
| `-letter-spacing` | `0` | Extra space between characters. |
| `-padding` | `0` | Padding around the text. |
| `-bg` | empty | Background rectangle colour. |
| `-separate` | false | One `<path>` per character, each with `id` and `data-char`. |
| `-kern` | true | Apply the font's kerning pairs. |
| `-precision` | `2` | Decimal places in path coordinates. |
| `-out` | `/tmp/common-skills/text-to-svg/text.svg` | Output path, or `-` for stdout. |
| `-list` | empty | List Google families matching a substring, or `*` for all 1946, then exit. |

## How it works

1. Load the font, from `-font-file` or from Google Fonts with a local cache.
2. `sfnt.Parse` reads the TrueType or CFF outlines.
3. For each rune, `GlyphIndex` then `LoadGlyph` returns the contour segments at
   the requested size, and `GlyphAdvance` plus `Kern` place the next character.
4. Segments become path commands: `MoveTo` to `M`, `LineTo` to `L`, `QuadTo` to
   `Q`, `CubeTo` to `C`. Each contour is closed with `Z`.
5. The viewBox is sized from the font's ascent and descent plus the line count.

Unlike the browser-based tools built on opentype.js, no mirroring step is
needed. `sfnt` already reports coordinates with y running downward, which is the
SVG convention.

## Notes

- A character missing from the font is skipped with a warning on stderr rather
  than failing the run.
- CJK families are large. Noto Serif KR is about 14 MB per weight, which is why
  downloads are cached. Latin families are usually under 300 KB.
- The CSS API serves woff2 to a browser and TrueType to everything else. This
  tool reads TrueType and CFF only, so it checks the extension it was given and
  says to use `-font-file` if that ever changes.
- Every contour is closed. Leaving them open is invisible under a fill but shows
  as missing edges the moment a stroke is applied.
- Output is one combined path by default. Use `-separate` when each character
  needs to be animated or coloured on its own.

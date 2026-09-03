# common-skills

Shared agent skills.

## Requirements

- Go 1.26 or later. Skills under `skills/` are Go projects and run with
  `go run`; they are not built into binaries.
- ffmpeg and ffprobe, optional, for audio duration and segment merging.

## Skills

| Skill | Description |
|---|---|
| [`ondoku3-tts`](skills/ondoku3-tts/) | Converts text to mp3 through the ondoku3.com advanced-tts API, with srt subtitles and an index.json cache. |
| [`unsplash-search`](skills/unsplash-search/) | Searches the Unsplash photo API for candidate images, with attribution and a used-photo ledger. |
| [`iconify-svg`](skills/iconify-svg/) | Searches Iconify and downloads icons as SVG, with colour and size applied at download time. |
| [`text-to-svg`](skills/text-to-svg/) | Renders text as SVG outline paths from a font's glyphs, with configurable size, fill, stroke and spacing. |
| [`writing-humanizer`](skills/writing-humanizer/) | Strips AI writing patterns from text, primarily Traditional Chinese, across 31 catalogued patterns. |

## Setup

    cp .env.template .env
    set -a; . ./.env; set +a

`unsplash-search` needs `UNSPLASH_ACCESS_KEY`. Every other skill needs nothing.
Nothing loads `.env` automatically, so source it as above.

## Conventions

See [AGENTS.md](AGENTS.md).

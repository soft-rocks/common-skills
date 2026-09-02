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

## Setup

    cp .env.template .env

## Conventions

See [AGENTS.md](AGENTS.md).

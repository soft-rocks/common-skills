---
name: edge-tts
description: Convert text to speech mp3 through the public endpoint behind Microsoft Edge's read-aloud feature. No account, no API key, no quota. Use when the user asks to read text aloud, synthesize speech, generate TTS, or narrate something.
---

# edge-tts

Source: `skills/edge-tts/scripts/main.go`, a Go module with its own `go.mod`.

Edge's read-aloud feature is backed by a public websocket endpoint. There is no
account, no key and no request quota, so this never stalls partway through a
long job.

## Usage

Always run through `go run`. Do not `go build` and do not leave a binary behind.

```bash
cd skills/edge-tts/scripts

go run . -text "text to synthesize" -out ./audio/p01.mp3
go run . -text-file script.txt -out ./audio/p01.mp3 -index ./audio/index.json
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-text` | empty | Text to synthesize. |
| `-text-file` | empty | Read the text from a file instead. Use this or `-text`, not both. |
| `-voice` | `fr-FR-VivienneMultilingualNeural` | Voice name. See `-list`. |
| `-rate` | `+0%` | Speaking rate offset, such as `+20%`. |
| `-pitch` | `+0Hz` | Pitch offset. |
| `-volume` | `+0%` | Volume offset. |
| `-out` | `/tmp/common-skills/edge-tts/<hash>.mp3` | Output mp3 path. |
| `-index` | `<out dir>/index.json` | Index file path. |
| `-max` | `380` | Characters per segment. Longer input is split at sentence ends. |
| `-retry` | `3` | Retries per segment. |
| `-edge-version` | empty | Force a `Sec-MS-GEC-Version`. Empty tries the known ones in order. |
| `-list` | false | List the 322 available voices, then exit. |
| `-locale` | empty | With `-list`, filter by locale prefix such as `zh-` or `en-US`. |

## Choosing a voice

The default is `fr-FR-VivienneMultilingualNeural`, and picking a French voice to
read Chinese is deliberate. This endpoint's Chinese options are weak: Taiwanese
Mandarin has only three voices, all tagged Friendly or Positive, which reads soft
for anything serious. The mainland accents are wrong for Taiwan and the
Cantonese voices are a different language.

The twelve voices with `Multilingual` in the name are the ones worth using. Each
is one voice actor's timbre synthesized across languages, so Chinese text comes
out in that person's voice with noticeably more range.

| Voice | Gender | Native |
|---|---|---|
| `en-US-AndrewMultilingualNeural` | male | US |
| `en-US-BrianMultilingualNeural` | male | US |
| `en-AU-WilliamMultilingualNeural` | male | AU |
| `de-DE-FlorianMultilingualNeural` | male | DE |
| `fr-FR-RemyMultilingualNeural` | male | FR |
| `it-IT-GiuseppeMultilingualNeural` | male | IT |
| `ko-KR-HyunsuMultilingualNeural` | male | KR |
| `en-US-AvaMultilingualNeural` | female | US |
| `en-US-EmmaMultilingualNeural` | female | US |
| `fr-FR-VivienneMultilingualNeural` | female | FR |
| `de-DE-SeraphinaMultilingualNeural` | female | DE |
| `pt-BR-ThalitaMultilingualNeural` | female | BR |

Keep one voice for one piece of work. Two voices in a single video is a defect,
not a style choice, so changing voice means re-recording the whole thing.

## Output

Each run writes three things next to `-out`:

- `<name>.mp3`
- `<name>.srt`, a single cue spanning `00:00:00,000 --> duration`
- `index.json`, recording hash, audio path, subtitle path, duration and voice

`tone` is always empty because this endpoint takes no tone instruction. `speed`
is derived from `-rate`, where `+0%` is 1, and `rate` and `volume` keep the raw
flag values.

## Splitting

Input over `-max` characters is split at sentence ends into `*_001.mp3`,
`*_002.mp3` and so on, each with its own subtitle and index entry. Full width
stops end a sentence on their own; ASCII stops count only when whitespace
follows, so a decimal such as 3.5 stays intact.

Segments are usually a signal that the source text wants breaking up at the
source rather than being stitched back together afterwards. The tool prints a
note on stderr when it splits.

## How it works

1. Compute `Sec-MS-GEC`: a Windows FILETIME rounded down to five minutes, concatenated with a fixed `TrustedClientToken`, hashed with SHA256 as uppercase hex.
2. Open the websocket with that token, a `Sec-MS-GEC-Version` and browser headers.
3. Send `Path:speech.config` to set the output format, `audio-24khz-48kbitrate-mono-mp3`.
4. Send `Path:ssml` wrapping the text.
5. Read binary frames. The first two bytes are the header length, big endian; frames whose header carries `Path:audio` hold audio, accumulated until `Path:turn.end`.

## The version string expires

This is the one part that breaks on its own. The endpoint rejects a
`Sec-MS-GEC-Version` it considers too old, and the symptom is a `403 Forbidden`
on the handshake.

Measured in September 2026: `130.0.2849.68` and `131.0.2903.86` are both
refused, `140.0.3485.54` works. The public `edge-tts` Python package hardcodes
130, so copying from it fails immediately.

`knownVersions` in main.go holds several versions and they are tried in order,
with the successful one reused for later segments in the same run. When every
one is refused, the error says what to do: check the current Edge version, pass
it with `-edge-version`, and add it to `knownVersions` once it works.

## Limits

- This is the undocumented endpoint the Edge front end uses. Use it for personal automation of what the browser already offers, not for bulk traffic.
- There is no quota, but do not hammer it in parallel. Segments are already spaced one second apart. Do not fan out across subagents or several shells.
- Duration comes from `ffprobe`. Without it, `duration` is 0 and subtitles fall back to an estimate of 0.15s per character.

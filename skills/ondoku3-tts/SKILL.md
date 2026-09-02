---
name: ondoku3-tts
description: Convert text to mp3 through the ondoku3.com advanced-tts beta API. Use when the user asks to read text aloud, synthesize speech, generate TTS, or mentions ondoku3.
---

# ondoku3-tts

Source: `skills/ondoku3-tts/scripts/main.go`, a Go module with its own `go.mod`.

## Usage

Always run through `go run`. Do not `go build` and do not leave a binary behind.

```bash
cd skills/ondoku3-tts/scripts
go run main.go -text "text to synthesize, 10 to 400 characters" \
  -voice Hugo \
  -tone "read this in a calm, formal tone" \
  -speed 1.2 \
  -pitch -0.5 \
  -model flash \
  -temperature 1 \
  -seed 18
```

### Flags

| Flag | Default | Description |
|---|---|---|
| `-text` | sample sentence | Text to synthesize. The server requires 10 to 400 characters, counted in runes rather than bytes. |
| `-voice` | `Hugo` | Voice name from the list below. An unknown name fails before the request is sent and prints the full list. |
| `-tone` | sample instruction | Tone or style instruction. |
| `-speed` | `1.2` | Speaking rate. |
| `-pitch` | `-0.5` | Pitch offset. |
| `-model` | `flash` | Model name. |
| `-temperature` | `1` | Sampling temperature. |
| `-seed` | `18` | Random seed. |
| `-out` | empty | Output mp3 path. When empty, writes to `/tmp/common-skills/ondoku3-tts/<job_id>.mp3`. |
| `-index` | `<out_dir>/index.json` | Index file path. When empty, writes `index.json` next to the audio. |
| `-poll-interval` | `2s` | Interval between job status polls. |
| `-poll-timeout` | `60s` | Poll timeout. |
| `-batch` | empty | Path to a JSON job file. Runs every job sequentially. |

The `-text` and `-tone` defaults are neutral smoke-test values. The tone
instruction is sent to the model and shapes the delivery, including accent and
register, so pass it explicitly for anything real.

### Batch runs

Write the job list to a JSON file under `/tmp/common-skills/ondoku3-tts/` and
pass it with `-batch`. Do not loop the command from a shell, because the script
owns the pacing and retries.

```bash
cat > /tmp/common-skills/ondoku3-tts/batch.json <<'JSON'
[
  {"text": "first line", "voice": "Hugo", "out": "audio/001.mp3"},
  {"text": "second line", "voice": "Anna", "pitch": 0, "out": "audio/002.mp3"}
]
JSON

go run main.go -batch /tmp/common-skills/ondoku3-tts/batch.json
```

The file is either a bare array or an object with a `jobs` key. Every field is
optional and falls back to the matching flag, so an entry can carry nothing but
`text`. Recognised fields: `text`, `voice`, `tone`, `speed`, `pitch`, `model`,
`temperature`, `seed`, `out`, `index`.

Pacing and failure handling. The timings are constants in `main.go`, not flags,
because they exist to stay under the rate limit:

- Jobs run one at a time, 15s apart, on top of the 3s between the chunks of a
  long job. Never run several instances in parallel or fan the list out across
  subagents.
- Every job is validated before the first request, so an unknown voice or a
  too-short line fails immediately rather than after minutes of synthesis.
- A failed job is retried twice, backing off 15s then 30s. The upstream model
  returns `tts_retry_exhausted` intermittently, and a retry usually clears it.
- When a job still fails, a batch records it and continues, then prints a
  summary and exits 1. A single job without `-batch` fails immediately.

### Voices

Taken from the `#voice-base-info` block embedded in the advanced-tts beta page,
mirrored in the `voiceGender` map in `main.go`.

Female, 12: Anna, Chloe, Ellis, Emma, Flora, Iris, Lena, Luna, Misa, Ruby, Sophie, Tina

Male, 13: Ash, Chris, Eden, Gray, Hope, Hugo, Kai, Leo, Noah, Reid, Roy, Sam, Yann

## Output

- With no `-out`, files go to `/tmp/common-skills/ondoku3-tts/` named by job id, so nothing is overwritten.
- With `-out`, pass a path inside the calling project so the audio, subtitles and index stay together and under version control.
- Each mp3 gets a matching single-cue `.srt` alongside it, spanning `00:00:00,000 --> duration`.
- The run prints the csrftoken it obtained, the job creation response including job id and poll url, each poll status, the signed audio url, and the final audio, subtitle and index paths with the measured duration.

## Index file

Default location is `index.json` next to the audio, overridable with `-index`.

```json
{
  "entries": [
    {
      "hash": "sha256 of the text",
      "text": "...",
      "audio": "/path/to.mp3",
      "subtitle_file": "/path/to.srt",
      "duration": 12.34,
      "voice": "Hugo",
      "tone": "...",
      "speed": 1.2,
      "pitch": -0.5,
      "model": "flash",
      "job_id": "...",
      "created_at": "RFC3339"
    }
  ]
}
```

An entry with a matching hash is moved to the end rather than duplicated, so the
hash works as a cache key. Duration comes from `ffprobe`; when ffprobe is
unavailable the value is estimated from the character count and still written.

## How it works

1. GET the beta page and read the Django `csrftoken` from the response cookies.
2. POST to `/api/advanced-tts/` with that cookie and an `X-CSRFToken` header.
3. A successful call returns `202` with `job_id`, `job_token`, `poll_url` and `min_poll_after_ms`.
4. Wait for `min_poll_after_ms`, then poll `poll_url` with an `X-Job-Token` header until the status is `succeeded` or `failed`.
5. Download the returned mp3 url, which is a time-limited signed link.

## Limits

- Text length is 10 to 400 characters server side. The script splits automatically: input over 380 characters is cut at sentence punctuation into segments of at most 380 characters, with a hard split for any single sentence that is longer. Segments are written as `*_001.mp3`, `*_002.mp3` and recorded separately in `index.json`. When ffmpeg is present the segments are also merged into `*_merged.mp3`.
- The API is rate limited. Do not parallelize it across subagents or threads. Segmentation already runs sequentially with at least three seconds between calls, so do not add another layer of concurrency around it.
- On a `quota_exceeded` response the script resplits the current segment using the reported remaining budget and retries. When the budget reaches zero, wait for it to reset.
- This is the undocumented API behind the ondoku3.com front end. Use it for personal automation of functionality the site already offers to visitors, not for bulk or abusive traffic.
- Signed audio urls expire after roughly 24 hours.

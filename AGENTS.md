# AGENTS.md

Guidance for coding agents working in this repository.

## Language

Everything in this repository is written in English: documentation, code,
comments, identifiers, commit messages, and file names. Generated output
follows the same rule, regardless of the language the request was written in.

## Writing style

These documents are read by people. Keep the prose plain and direct.

Do not use:

- Emoji, anywhere.
- Bold or italics for emphasis. Bold is for table headers and term definitions.
- Quotation marks for emphasis. Quotes are for quotations and literal strings.
- The "this is not X, it is Y" construction.
- Stacked parallel clauses or three-item rhythms added for effect.
- Filler openers such as "In today's world" or "It is worth noting that".
- Summary paragraphs that restate what the section already said.

Prefer short sentences, concrete nouns, and one idea per paragraph. If a clause
can be removed without losing meaning, remove it.

## Repository layout

    skills/<name>/          one skill per directory
    skills/<name>/SKILL.md  frontmatter (name, description) plus usage docs
    skills/<name>/scripts/  supporting code

## Skills

- Every skill directory carries its own SKILL.md.
- Paths inside a SKILL.md are relative to the repository root.
- Go skills run with `go run`. Do not `go build`, and do not commit binaries.
- A skill must not depend on directories outside its own folder.

## Scripts

Every script is a command line tool. Drive it with flags. Do not edit source to
change an input.

- Read all input from flags or stdin. Nothing that varies between runs is hardcoded.
- Write results to stdout and diagnostics to stderr. Exit non-zero on failure.
- Never prompt. A run has to complete unattended.
- Keep flag names lowercase with dashes, and give every flag a default that works.

### Batch runs

Do not loop a command from the shell. Write the job list as JSON under
`/tmp/common-skills/<skill-name>/` and pass the path to the script with a
`-batch` flag:

```bash
cat > /tmp/common-skills/ondoku3-tts/batch.json <<'JSON'
[
  {"text": "first line", "voice": "Hugo", "out": "out/001.mp3"},
  {"text": "second line", "voice": "Anna", "out": "out/002.mp3"}
]
JSON

go run main.go -batch /tmp/common-skills/ondoku3-tts/batch.json
```

A job file stays inspectable after the run, and long or multiline text avoids
shell quoting problems. The script owns the loop, so it also owns rate limiting
and retries. Skills that talk to a rate limited API process the list
sequentially and never fan out across subagents or threads.

## Requirements

- Go 1.26 or later.
- ffmpeg and ffprobe, optional, used for audio duration and merging.

## Environment

Copy `.env.template` to `.env` for local values. `.env` is gitignored and must
never be committed. Add new variables to `.env.template` with an empty value
and a short comment.

## Temporary files

Write scratch files under `/tmp`, never inside the repository. Group them in a
named subdirectory, `/tmp/common-skills/<skill-name>/`. Skills follow the same
rule: `ondoku3-tts` writes to `/tmp/common-skills/ondoku3-tts/` unless `-out`
says otherwise.

Never place scratch output in the working tree, where it can be committed by
accident.

## Commits

- This repository commits as soft-rocks <hamza@soft.rocks>, set in local git
  config. Do not change it and do not fall back to the global identity.
- Write commit messages in English, imperative mood, no emoji.
- Do not commit or push unless asked.

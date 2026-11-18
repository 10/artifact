---
name: artifact
description: Publish a file to a shareable URL with the `artifact` CLI. Use when the user asks to publish, share, upload, or get a link for something — an HTML report, Markdown doc, screenshot, screen recording, PDF, or log.
---

# Artifact

`artifact` publishes a local file and prints its URL.

**Publishing is the user's call, every time.** A published URL is public to
anyone holding it, and the files worth publishing are exactly the ones that tend
to carry secrets — an HTML report with a hostname in it, a design doc with an
internal URL, a log with a token in a stack trace. You cannot reliably tell from
the outside, so the decision stays with the user.

## Publish

**1. Get the ask.** Publish the file the user named. When something would be
easier to review as a link and they have not asked, offer in one line and stop
here:

```
That trace is 4,000 lines. Want me to publish it so you can open it in a browser?
```

*Done when:* the user named a file to publish, or accepted an offer.

**2. Name the file for what it is.** The extension alone decides how the URL
behaves — `build.log` displays in the browser, `build` downloads as a binary
blob. Rename before publishing, not after.

| Content | Extension | Result |
| --- | --- | --- |
| Report, standalone page | `.html` | Renders as the page |
| Doc, plan, notes | `.md` | Renders with tables, task lists, code blocks |
| Screenshot | `.png` `.jpg` | Displays inline |
| Screen recording | `.mp4` `.webm` | Plays, and seeks |
| Log, data, source | `.log` `.txt` `.json` `.go` `.py` | Displays as text |

HTML must be self-contained: inline the CSS and JS, because relative asset
paths resolve to nothing once published.

*Done when:* the extension matches the content.

**3. Publish and capture.**

```bash
URL=$(artifact report.html) || exit 1
```

Stdout carries the URL alone — that is what makes command substitution safe.
Progress, warnings, and errors all go to stderr.

*Done when:* the command exited 0 and `$URL` holds one URL.

**4. Report the link.** Give the human the URL and one line naming what is
behind it.

```
Published the failing test log: https://artifact.example.com/aB3xK9qZ
```

*Done when:* the human has a URL they can open and knows what it is.

## Publish to a stable URL

A plain publish gets a random ID. When the same thing will be republished —
a nightly report, a status page, anything a human might bookmark — claim a
slug once and update it thereafter. The URL never changes, and each update
adds a version that stays reachable at `@1`, `@2`, and so on.

```bash
artifact up --slug daily-report report.md    # first time
artifact update daily-report report.md       # every time after
```

Slugs are lowercase alphanumerics and hyphens, 3–64 characters, and **never
exactly 8 characters**. A slug is claimed permanently, so `slug_taken` means
pick another name or `update` the existing artifact.

## When it fails

The exit code says what to do. Read it before retrying.

| Exit | Meaning | Do this |
| --- | --- | --- |
| `0` | Published | Continue |
| `1` | I/O error, dropped connection, or server 5xx | Confirm the path exists and is readable. Retry once on a dropped connection or a 5xx — the CLI does not retry on its own |
| `2` | Bad usage, or no endpoint or token configured | Read stderr: fix the command, or report that Artifact is not set up here — a hostname cannot be guessed |
| `3` | Token rejected | Stop. Every further call fails identically |
| `4` | No such artifact | Check the ID or slug |

Stderr carries the reason in one line. Surface it rather than the exit code
alone.

## What counts as being asked

Step 1 gates `up`. The two commands that touch something already published need
the same explicit request, and neither has an offer path:

- **`update`** — the user named both the artifact and the new content. It
  replaces what is live at a URL that may already be circulating.
- **`rm`** — the user asked for it by ID or slug. Deletion is irreversible and
  the ID is retired forever rather than reissued.

A request covers the whole task it was made for: told to publish four
screenshots, publish all four without asking again. It expires with that task.

Publish exactly what was named. When a request is broad — "publish the test
output" — and several files could match, name the one you picked in your reply
so a wrong guess is visible immediately.

## Reference

Full command and flag list, the complete content-type table, the URL scheme
including `/raw` and `@version`, and the `curl`-only form for environments
without the CLI: [REFERENCE.md](REFERENCE.md).

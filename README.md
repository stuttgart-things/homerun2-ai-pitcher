# homerun2-ai-pitcher

homerun2 pitcher that turns the daily [scout](https://github.com/stuttgart-things/homerun2-scout)
digest and the open findings of the
[schedule-pitcher](https://github.com/stuttgart-things/homerun2-schedule-pitcher)
into a short AI summary via [OpenRouter](https://openrouter.ai), and pitches it
into homerun2 through [omni-pitcher](https://github.com/stuttgart-things/homerun2-omni-pitcher).

> **Status: prototype.** Design and decisions:
> [stuttgart-things/homerun-library#135](https://github.com/stuttgart-things/homerun-library/issues/135).
> One run pitches one message, so it runs as a CronJob, e.g. at 07:45 after scout's own
> 07:00 digest.

## How it works

```
CronJob 07:45
  ├─ GET  scout            /analytics/digest?schedule=daily   (counts, previous window, top systems)
  ├─ GET  schedule-pitcher /api/findings?status=open          (optional: open + acknowledged findings)
  ├─ POST openrouter       /chat/completions                  (summary; model openrouter/free by default)
  ├─ POST openrouter       /chat/completions                  (ticker lines for the LED matrix)
  ├─ POST omni-pitcher     /pitch                             (summary: system ai-digest, severity info)
  └─ POST omni-pitcher     /pitch  x up to 4                  (one per ticker line: system ai-ticker)
```

### Ground rules

- **The numbers are scout's, never the model's.** The title is scout's digest title
  (`[AI] 24h: 20 msgs 0 err 0 crit`), and scout's digest text plus the findings list
  are appended verbatim under the AI text. The model only interprets: what needs
  attention, what changed, what belongs together.
- **The AI never decides severity or delivery.** Every message is `info`.
- **Fail-open.** If OpenRouter is slow, down, rate-limited or no key is set, the run
  still pitches: title `[AI n/a] …`, the reason, and the plain digest. Only a missing
  scout digest fails the run, because then there is nothing to summarise.
- **Marked.** Every AI text carries `AI-generated summary (model …)`. With the router
  model `openrouter/free` this names the free model that actually answered.
- **No feedback loop.** scout must exclude `system: ai-digest` from its digests
  (`DIGEST_EXCLUDE_SYSTEMS=ai-digest`).

Example (fallback, no key):

```
[AI n/a] 24h: 20 msgs 0 err 0 crit

AI summary not available (no OPENROUTER_API_KEY). The plain scout digest follows.

daily digest 2026-10-13 20:25 - 2026-10-14 20:25 (Europe/Berlin)
Messages: 20 (previous 34, -14)
Errors: 0 (previous 0, +0), critical: 0 (previous 0, +0)
Severities: warning 6, success 10, info 4
Top systems: homerun2-git-pitcher 10 (0 alerts), msteams 7 (0 alerts), homerun2-schedule-pitcher 3 (0 alerts)

Open findings:
- [warning] Vault token tekton-vault-labda (token-ci-packer-labda) expires in 25 days (2026-11-09)
- ...
```

## Ticker for the LED matrix

Long text is useless on a 64x64 matrix. After the summary, a second model call asks for
up to `TICKER_MAX` short lines (default 4 x 40 characters), and **each line is pitched as its
own message** (`system: ai-ticker`, `tags: ai,ticker`, severity `info`), `TICKER_DELAY` apart:

```
3 Vault-Tokens laufen am 9.11. ab
31 Meldungen, Warnungen deutlich weniger
Keine Fehler, nichts kritisch
```

The lines are checked, not trusted:

- folded to plain ASCII (`ä` → `ae`, `–` → `-`, emojis dropped), because the matrix BDF fonts
  have no umlauts;
- list markers and quotes stripped, cut at a word boundary, duplicates dropped;
- **a line with a number that is not in the input is dropped.** Allowed are the numbers in
  scout's digest and the findings, plus counts from 1 to the number of open findings, so
  grouping ("3 Vault-Tokens") works but "42 neue PRs" does not.

Without a usable line (model down, nothing passed the checks), the matrix gets scout's own
digest title as the only line, tagged `ticker,digest` and not marked as AI.

A matching led-catcher display rule:

```yaml
displayRules:
  ai-ticker:
    systems: [ai-ticker]
    severity: [INFO]
    kind: text
    text: "{{ title }}"
    font: 6x10.bdf
    duration: 8
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `SCOUT_URL` | (required) | scout base URL, e.g. `http://homerun2-scout.homerun2.svc.cluster.local` |
| `SCOUT_TOKEN` | (required) | scout API bearer token |
| `DIGEST_SCHEDULE` | `daily` | `daily` or `hourly`: which scout digest window |
| `SCHEDULE_PITCHER_URL` | (empty = skip) | schedule-pitcher base URL |
| `SCHEDULE_PITCHER_TOKEN` | | schedule-pitcher API bearer token |
| `MAX_FINDINGS` | `20` | findings passed to the model (worst first) |
| `OPENROUTER_API_KEY` | (empty = fallback) | OpenRouter API key |
| `OPENROUTER_MODEL` | `openrouter/free` | an OpenRouter model id, or a **comma-separated list** tried in order (OpenRouter `models` fallback), e.g. `google/gemma-4-31b-it:free,nvidia/nemotron-3-super-120b-a12b:free,openrouter/free`. `openrouter/free` alone may route to very small models (2.6B) that misread findings |
| `OPENROUTER_BASE_URL` | `https://openrouter.ai/api/v1` | OpenAI-compatible endpoint |
| `LLM_TIMEOUT` | `90s` | timeout of the model call |
| `LLM_MAX_TOKENS` | `4000` | token cap of the answer **including a reasoning model's thinking** (`reasoning.effort: low`, thinking excluded from the answer). An answer cut by the cap is trimmed to its last full sentence and marked |
| `SUMMARY_LANGUAGE` | `German` | language of the AI text |
| `LLM_RETRIES` | `2` | more tries after a failed model call (HTTP error, empty or cut-off answer); back-off `LLM_RETRY_DELAY` (`5s`), doubled each time. A missing key is not retried |
| `TICKER_ENABLED` | `true` | also pitch short ticker lines for the LED matrix |
| `OPENROUTER_TICKER_MODEL` | `OPENROUTER_MODEL` | model for the ticker lines |
| `TICKER_MAX` | `4` | at most this many ticker lines |
| `TICKER_MAX_CHARS` | `40` | at most this many characters per line |
| `TICKER_SYSTEM` | `ai-ticker` | `system` of the ticker messages |
| `TICKER_DELAY` | `3s` | pause before each ticker pitch, so the matrix shows them one by one |
| `PITCHER_URL` | (required unless dry run) | omni-pitcher base URL; `/pitch` is appended |
| `PITCHER_TOKEN` | (required unless dry run) | omni-pitcher bearer token |
| `PITCH_SYSTEM` | `ai-digest` | `system` of the pitched message |
| `PITCH_AUTHOR` | `homerun2-ai-pitcher` | `author` of the pitched message |
| `DRY_RUN` | `false` | print the message as JSON instead of pitching it |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

> The platform's notification-catcher forwards grafana-format messages to Teams only from
> `warning` up and `success`. An `info` message on `system: ai-digest` needs its own output
> there.

## Run locally (env vars + go)

Only Go and a kubeconfig for the cluster that runs homerun2 are needed. `DRY_RUN=true`
prints the message instead of pitching it, so no omni-pitcher token is needed.

```bash
export KUBECONFIG=~/.kube/platform-sthings

# 1. scout and schedule-pitcher on localhost
kubectl -n homerun2 port-forward svc/homerun2-scout 18080:80 &
kubectl -n homerun2 port-forward svc/homerun2-schedule-pitcher 18081:80 &

# 2. their API tokens from the cluster, plus your OpenRouter key
export SCOUT_URL=http://localhost:18080
export SCOUT_TOKEN=$(kubectl -n homerun2 get secret homerun2-scout-token -o jsonpath='{.data.auth-token}' | base64 -d)
export SCHEDULE_PITCHER_URL=http://localhost:18081
export SCHEDULE_PITCHER_TOKEN=$(kubectl -n homerun2 get secret homerun2-schedule-pitcher-token -o jsonpath='{.data.auth-token}' | base64 -d)
export OPENROUTER_API_KEY=sk-or-...        # leave unset to see the fail-open fallback
# export OPENROUTER_MODEL=openrouter/free  # default
export DRY_RUN=true

# 3. run
go run .
```

To really pitch, port-forward omni-pitcher as well and drop `DRY_RUN`:

```bash
kubectl -n homerun2 port-forward svc/homerun2-omni-pitcher 18082:80 &
export PITCHER_URL=http://localhost:18082
export PITCHER_TOKEN=$(kubectl -n homerun2 get secret homerun2-omni-pitcher-token -o jsonpath='{.data.auth-token}' | base64 -d)
unset DRY_RUN
go run .
```

Without a cluster, the tests run the whole flow against fake scout, schedule-pitcher and
OpenRouter servers:

```bash
go test ./...
golangci-lint run ./...
```

## Deployment

`kcl/` renders a CronJob (default `45 7 * * *`, Europe/Berlin, non-root, read-only root
filesystem, no service account token) and its Secret `homerun2-ai-pitcher` with the keys
`scout-token`, `schedule-pitcher-token`, `pitcher-token` and `openrouter-api-key`. The
schedule-pitcher and OpenRouter keys are optional: without them the run degrades instead of
failing. Each release pushes the rendered base as `ghcr.io/stuttgart-things/homerun2-ai-pitcher-kustomize:<version>`,
with placeholder secret values and the image set to the released tag.

```bash
kcl run kcl/main.k -D config.scoutToken=x -D config.pitcherToken=y
```

The image is built with [ko](https://ko.build) (`.ko.yaml`, no Dockerfile) and released by
semantic-release on conventional commits (`fix:`, `feat:`).

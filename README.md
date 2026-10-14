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
  ├─ POST openrouter       /chat/completions                  (model openrouter/free by default)
  └─ POST omni-pitcher     /pitch                             (system ai-digest, severity info)
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
| `OPENROUTER_MODEL` | `openrouter/free` | any OpenRouter model id; `openrouter/free` routes to an available free model |
| `OPENROUTER_BASE_URL` | `https://openrouter.ai/api/v1` | OpenAI-compatible endpoint |
| `LLM_TIMEOUT` | `90s` | timeout of the model call |
| `SUMMARY_LANGUAGE` | `German` | language of the AI text |
| `PITCHER_URL` | (required unless dry run) | omni-pitcher base URL; `/pitch` is appended |
| `PITCHER_TOKEN` | (required unless dry run) | omni-pitcher bearer token |
| `PITCH_SYSTEM` | `ai-digest` | `system` of the pitched message |
| `PITCH_AUTHOR` | `homerun2-ai-pitcher` | `author` of the pitched message |
| `DRY_RUN` | `false` | print the message as JSON instead of pitching it |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

> The platform's notification-catcher forwards grafana-format messages to Teams only from
> `warning` up and `success`. An `info` message on `system: ai-digest` needs its own output
> there.

## Development

```bash
go test ./...
golangci-lint run ./...

# Dry run against a cluster (port-forward scout and schedule-pitcher first)
SCOUT_URL=http://localhost:18080 SCOUT_TOKEN=... \
SCHEDULE_PITCHER_URL=http://localhost:18081 SCHEDULE_PITCHER_TOKEN=... \
OPENROUTER_API_KEY=... DRY_RUN=true go run .
```

The image is built with [ko](https://ko.build) (`.ko.yaml`, no Dockerfile) and released by
semantic-release on conventional commits (`fix:`, `feat:`).

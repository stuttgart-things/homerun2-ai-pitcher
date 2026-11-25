# homerun2-ai-pitcher

homerun2 pitcher that turns the daily [scout](https://github.com/stuttgart-things/homerun2-scout)
digest, what happened in the same window (merged PRs, releases, outages: scout's messages) and
the open findings of the
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
  ├─ GET  scout            /analytics/messages                (optional: git activity, alerts, outages; scout v0.12+)
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
- **Counts are computed, not generated.** Merged PRs, releases, commits, failed workflow runs,
  issues and outages (with start and duration) are counted by code from scout's messages, shown
  under the digest as plain lines, and given to the model as exact numbers.
- **Titles are data.** PR, issue, release and alert titles reach the model only as JSON values,
  the prompt says never to follow instructions inside them, and a number that appears only in a
  PR or issue title is not allowed on the ticker. Message texts never reach the model.
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

## Modes

`MODE` picks what a run summarises and sets the defaults of the window, the pitched `system`,
the ticker and the activity; an env var that is set still wins. Each mode runs as its own CronJob.

| `MODE` | Typical schedule (Berlin) | Window | Focus | `system` | Ticker | Activity |
|---|---|---|---|---|---|---|
| `morning` (default) | 07:45 Mon–Fri | daily | needs attention today, what happened overnight | `ai-digest` | yes | yes |
| `evening` | 18:45 Mon–Fri, before quiet hours | daily | what stays open, what is due tomorrow, what got done | `ai-evening` | no | yes |
| `week-ahead` | Mon 07:50 | daily | findings due in the next 7 days, by date | `ai-week-ahead` | no | no |
| `weekly` | Fri 15:00 | weekly (scout v0.12+) | releases, PRs, outages of the week vs. the week before | `ai-weekly` | yes | yes |
| `cluster-analysis` | daily 07:30 | `LOOKBACK_DAYS` (7) | findings of the fleet (K8sGPT, endpoint probes, VMs) from S3, by **Claude Code** — see below | `ai-cluster-analysis` | no | no |
| `incident` | always on (Deployment) or per lag (KEDA ScaledJob) | per alert, windows of 15 min | repeats per fingerprint, alerts that start together — see [Incidents](#incidents-modeincident-5) | `ai-incident` | no | no |

The title says which run it is: `[AI] 24h: …` for morning (unchanged), `[AI evening] …`,
`[AI week ahead] …`, `[AI weekly] 7d: …`; tags end in the mode (`ai,digest,daily,evening`).
scout must exclude every pitched system from its digests:
`DIGEST_EXCLUDE_SYSTEMS=ai-digest,ai-evening,ai-week-ahead,ai-weekly,ai-ticker,ai-incident`.

## Cluster analysis (`MODE=cluster-analysis`)

Once a day, **Claude Code** assesses the findings of the **whole fleet**
(#12, #18, tracking stuttgart-things/stuttgart-things#4556). It reads no scout:
its input is the S3 archive that homerun2-s3-catcher fills (bucket
`k8s-cluster-report`), and it acts on nothing. Sources (`ANALYSIS_SOURCES`):

| Source | Systems | Snapshot payload |
|---|---|---|
| `k8sgpt` | `k8sgpt-<cluster>` | K8sGPT Result CRs (k8s-pitcher snapshot collector) |
| `endpoints` | `endpoints-<cluster>` | `findings/v1` (k8s-pitcher endpoint probes) |
| `vm` | `vm-<host>` | `findings/v1` (Ansible health report) |

`findings/v1` (contract: stuttgart-things/stuttgart-things#4556): a message
tagged `snapshot` whose payload is
`{"format":"findings/v1","source":…,"target":…,"count":n,"items":[{"fingerprint","kind","name","namespace","parent","severity","texts"}]}`
with the open findings only. A new source needs no code: add it to
`ANALYSIS_SOURCES`.

**Versions as context (#21).** The latest `inventory-<cluster>` snapshot
(`inventory/v1`) of each cluster is condensed in code into facts:
- Kubernetes and kubelet versions, and kubelets on another minor or on several versions
- images with `:latest` or no tag
- the same image repository, chart or Crossplane package in different versions on two clusters or more
- HelmReleases, Applications and packages that are not ready

Claude gets them as `versions` context; they are not findings (no new or
resolved, no severity). The report renders them as the section **Versionen**.

**GitHub issues (#22, `ISSUES_MODE`).** After the assessment, in code:
- **Candidates:** a topic with `action` issue or pr-proposal and confidence medium or high, a topic holding a **new** finding of severity error or critical (whatever the model said), and every such finding no topic covers. One issue per topic, labels `fleet-analysis`, `severity/<worst>`, `area/<kubernetes|endpoints|vm>`, plus `fleet-analysis/pr-proposal`.
- **Dedupe:** the body carries `<!-- fleet-fp: … -->` and `<!-- fleet-sys: … -->`. An open `fleet-analysis` issue with an overlapping fingerprint is never filed twice; it gets a "Besteht weiter" comment at most every `ISSUES_COMMENT_EVERY`, or at once when new findings join, and its markers grow.
- **Resolved:** all fingerprints gone while every system of the issue still reports → a comment and the label `fleet-analysis/resolved` (closed with `ISSUES_CLOSE_RESOLVED`). If it comes back: "Wieder aufgetreten", label removed.
- **Visibility:** the report lists every action under **GitHub-Issues**. `DRY_RUN` forces the issues into dry-run.

```
k8s-cluster-report (read-only user)
  └ per cluster: the latest snapshot + one per day of LOOKBACK_DAYS
      → condensed input (deterministic): open findings with fingerprint,
        first day seen, days present, recurring, new vs. the last report,
        resolved since, the same error on several clusters, stale clusters
          → claude -p (no tools, no MCP, no settings; --json-schema)
              → summary + topics (fingerprints, clusters, assessment,
                proposal, confidence, action: none|issue|pr-proposal)
                  validated: unknown fingerprints/clusters dropped
→ k8s-cluster-analysis: analysis/date=<day>/<run>.json + .md (writer user)
→ one pitch, system ai-cluster-analysis, link REPORT_URL
```

- **Numbers and severity come from the input, never from the model**
  (ground rules above): `error` with a new finding of severity error or
  critical, `warning` with other new findings or a system without a current
  snapshot, `success` with none open, else `info`.
- **The model failing is not fatal:** the report carries the numbers and the
  error, and the pitch says `[AI n/a]`.
- **The previous report is the baseline** for new and resolved (its
  `openFindings`). The first run marks nothing new.
- The report's Markdown is rendered by this code, not by the model. The UI
  of homerun2-s3-catcher shows it under *Analyses*.

| Variable | Default | |
|---|---|---|
| `S3_ENDPOINT` | **required** | `host[:port]` |
| `S3_USE_SSL` / `S3_INSECURE` / `S3_REGION` | `true` / `false` / `us-east-1` | |
| `ARCHIVE_BUCKET`, `ARCHIVE_ACCESS_KEY`, `ARCHIVE_SECRET_KEY` | `k8s-cluster-report`, **required** | read-only user |
| `REPORT_BUCKET`, `REPORT_ACCESS_KEY`, `REPORT_SECRET_KEY` | `k8s-cluster-analysis`, **required** | writer of the reports |
| `ANALYSIS_SOURCES` | `k8sgpt,endpoints,vm` | sources, archive prefix `system=<name>-<target>/`; `name=prefix` sets another prefix |
| `ANALYSIS_LABS` | — | groups the report by lab (#26): `LabUL=platform-sthings,*-labul;LabDA=sthings-app*,labda-*`, target name globs, first match wins; per-lab table, lab column, labs per topic, per-lab line in the pitch. Empty: no grouping |
| `ANALYSIS_STALE_AFTER` | `vm=26h` | per-source override of `STALE_AFTER` (#28): VM health reports come daily. Empty: `STALE_AFTER` for all |
| `ANALYSIS_KNOWN_FINDINGS` | — | path of a known-findings file (stuttgart-things#4831): findings the team accepted, with `reason`, `owner`, `expires` (YYYY-MM-DD) and either a `fingerprint` or a `match` (globs on `source`, `target`, `kind`, `namespace`, `name`, `text`). Matching findings are marked `known`: never new, not counted as open, no topic, no GitHub issue, and they keep no issue open. The report lists them, expired acceptances (their findings count as open again) and entries that matched nothing. Validated strictly at start: unknown fields or a missing field fail the run. Empty: none |
| `LOOKBACK_DAYS` | `7` | days of snapshots per system |
| `ANALYSIS_INVENTORY_PREFIX` / `INVENTORY_STALE_AFTER` | `inventory-` / `36h` | the clusters' inventory (k8s-pitcher#108) as context, #21 |
| `ISSUES_MODE` | `off` | `off`, `dry-run` (report says what it would file), `on` — GitHub issues from the analysis, #22 |
| `ISSUES_REPO` / `GITHUB_TOKEN` | `stuttgart-things/stuttgart-things` / — | fine-grained token, issues read/write; required for `on` |
| `ISSUES_MAX_PER_RUN` / `ISSUES_COMMENT_EVERY` / `ISSUES_CLOSE_RESOLVED` | `5` / `168h` / `false` | new issues per run, one "still open" comment per issue per window, close once resolved |
| `STALE_AFTER` | `3h` | snapshots come hourly |
| `MAX_FINDINGS_PER_CLUSTER` | `50` | passed on per system (new and recurring first) |
| `REPORT_URL` | — | link in the pitch, e.g. the portal's `/archive/?tab=analyses` |
| `CLAUDE_BIN` / `CLAUDE_MODEL` | `claude` / `sonnet` | |
| `CLAUDE_MAX_BUDGET_USD` | `2` | `--max-budget-usd` per call |
| `CLAUDE_TIMEOUT` / `CLAUDE_ATTEMPTS` | `10m` / `2` | |
| `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` | **one required** | read by the CLI: a subscription token (`claude setup-token`) for testing, an API key for production |
| `PITCHER_URL`, `PITCHER_TOKEN`, `PITCH_AUTHOR`, `SUMMARY_LANGUAGE`, `DRY_RUN` | as above | |

Needs the `claude` CLI in the image: the `-claude` image variant (#13).
`DRY_RUN=true` builds the report, calls the model and prints report and
message without storing or pitching.

## Incidents (`MODE=incident`, #5)

A **stream consumer** instead of a CronJob run. It reads the raw bus (`alerts`, `messages`) with its own consumer group, `homerun2-ai-pitcher-incident`, and turns repeats and bursts into a few `info` messages marked AI. It does not read the notification-catcher's output: the patterns are in the raw events, including the repeats.

**How far it acts:** levels 0 and 1 of stuttgart-things/homerun-library#135 (2026-10-22). It reads, correlates and summarises. It never acts on a cluster. The original messages are delivered unchanged, whatever this mode does.

### 1. Correlation per fingerprint, no model

- **Key:** omni-pitcher's `/pitch/alertmanager` tags every alert with `status=firing|resolved,fingerprint=<fp>`, and the fingerprint is the key. Every other message is keyed by `system` plus its title with numbers and ids folded out, so "Pod web-7d9f8c restarted 3 times" and "Pod web-5b2a1e restarted 4 times" share one key.
- **State per key** lives in Redis (hash `homerun2-ai-pitcher:incident:state`): first seen, last seen, occurrences, clusters (the `cluster` label, else the system, which is the Alertmanager receiver), namespaces, severity and resolved. Because it lives in Redis, a restarted pod or the next Job picks it up.
- **Repeats:** the first message is the original card and is left alone. From the second occurrence on, the mode sends at most one message per key per `HINT_COOLDOWN` (default 6h), for example:
  `[AI] NodeClockNotSynchronising: still firing since 2026-11-20 08:00, on 2 clusters, never resolved`
  The cluster count is taken over every open alert with the same alertname, so the same alert from two Alertmanagers counts as 2 clusters.
- **Resolved** closes the state. If the mode said something about the key, it adds one `[AI] …: resolved after 13h` message, which also gives the number of alert messages and AI updates. If it fires again, a new episode starts; a key that keeps resolving and firing is reported as `resolved N times before (flapping)`.
- **Stale:** a key not seen for `INCIDENT_STALE_AFTER` (26h) starts a new episode. Plain messages never resolve, and a silenced alert may not either.
- **Tags** are `ai,incident,phase=ongoing|resolved|window,key=…`. They never include `status=` or `fingerprint=`, so the catcher does not take the message for an alert.

### 2. Incidents per window, one model call

- Alerts that open an episode go into a window. The window closes 15 min after its first alert (`INCIDENT_WINDOW`) or after 5 quiet minutes (`INCIDENT_QUIET`).
- At closing, alerts that share an alertname or a cluster/namespace form a group. A group with at least `INCIDENT_MIN_ALERTS` (2) keys becomes one message: `[AI] Incident on sthings-app/shop: 3 alerts within 4m`, followed by the list of alerts.
- **One model call per window**, not per message, asks for the probable common cause per group.
  - A line with a number that is not in the input is dropped, as with the ticker. Numbers never come from the model.
  - **Fail-open:** without a key, with the model down or with no usable line, the deterministic summary goes out alone, marked `[AI n/a]` when the model failed.

### Never its own output

`HINT_EXCLUDE_SYSTEMS` (default `ai-*,scout-digest`) is never correlated. A trailing `*` is a prefix match, so every `ai-*` system is covered: the digests, the tickers, the cluster analysis and this mode's own `ai-incident` messages.

### Run as Deployment or Job

| `INCIDENT_RUN` | Use |
|---|---|
| `loop` (default) | a Deployment with 1 replica (`Recreate`): blocks on the streams and closes windows on time. Works without KEDA. |
| `once` | a Job: drains the streams, waits until the open window has closed, exits. For a KEDA `ScaledJob` with the `redis-streams` trigger on `lagCount` (Redis ≥ 7). `pendingEntriesCount` would only count entries a consumer has already read, so new alerts would never start a Job. |

KCL: `config.incidentEnabled=true` and `config.incidentRunAs=deployment|scaledjob`. Set the Redis password with `config.redisPassword` (Secret key `redis-password`). With `scaledjob`, KEDA must be installed, and the consumer group has to exist before the first trigger: run `XGROUP CREATE <stream> homerun2-ai-pitcher-incident $ MKSTREAM`, or start the Deployment once.

A consumer that dies without acknowledging leaves its entries pending. After `INCIDENT_CLAIM_IDLE` (5m) the next consumer takes them over. Entries are acknowledged after processing even when the pitch failed (fail-open: a lost AI message is better than a backlog). A failed message is not counted against the cooldown, so the next occurrence tries again.

### Measuring it (#135)

The measure is Teams cards per real incident, before and after. Each closed episode logs `incident resolved … cards=<alert messages on the bus> aiMessages=<AI updates>`, and the resolved message repeats both numbers.
- **Before:** `cards`, which is what the outputs show today, one card per Alertmanager repeat.
- **After:** the first card, the resolved card and `aiMessages`, once the catcher sends the repeats of a fingerprint elsewhere than Teams, for example via `repeat_interval` or a `match.status` rule. That change is a catcher config change, made in a reviewed PR.

### Settings

| Variable | Default | Meaning |
|---|---|---|
| `REDIS_ADDR` / `REDIS_PORT` / `REDIS_PASSWORD` | – / `6379` / – | the bus |
| `INCIDENT_STREAMS` | `alerts,messages` | streams to read; the group is created with `MKSTREAM` from the newest entry on |
| `INCIDENT_GROUP` / `INCIDENT_CONSUMER` | `homerun2-ai-pitcher-incident` / hostname | consumer group and name |
| `INCIDENT_RUN` | `loop` | `loop` or `once` |
| `HINT_EXCLUDE_SYSTEMS` | `ai-*,scout-digest` | never read |
| `HINT_SEVERITIES` | `error,critical` | severities of **plain** messages that are tracked; alerts are always tracked |
| `HINT_COOLDOWN` | `6h` | at most one "still firing" per key |
| `INCIDENT_MIN_OCCURRENCES` | `2` | from which occurrence a key is worth a message |
| `INCIDENT_STALE_AFTER` / `INCIDENT_RETENTION` | `26h` / `168h` | new episode after silence / forget closed states |
| `INCIDENT_WINDOW` / `INCIDENT_QUIET` / `INCIDENT_MIN_ALERTS` | `15m` / `5m` / `2` | incident window |
| `INCIDENT_AI` | `true` | the model call per window (also needs `OPENROUTER_API_KEY`) |
| `INCIDENT_AI_BUDGET` | `2m` | cap of that call, retries included; split fairly over the model list (`LLM_TIMEOUT` per model, #11), so the stream never waits longer |
| `INCIDENT_TIMEZONE` | `Europe/Berlin` | times in the messages |
| `INCIDENT_STATE_PREFIX` | `homerun2-ai-pitcher:incident` | Redis keys of the state |
| `PITCH_SYSTEM` | `ai-incident` | system of the messages |

`DRY_RUN=true` prints the messages instead of pitching them. It still consumes and keeps state, so use a group of its own (`INCIDENT_GROUP`, `INCIDENT_STATE_PREFIX`) when trying it against the live bus.

## Activity: what happened in the window

With scout v0.12+ (`/analytics/messages`), each run reads the messages of the digest's window:

- **Git activity** of `MESSAGES_SYSTEMS` (default `homerun2-git-pitcher`), per repository: PRs
  merged / opened / closed unmerged with up to `MAX_TITLES` merged titles, releases, pushes and
  commits (from the push message text, which stays here), failed workflow runs, issues opened /
  closed (once git-pitcher pitches `issues` events).
- **Outages**: every system with an `error` or `critical` message, except `OUTAGE_EXCLUDE_SYSTEMS`
  (default `homerun2-git-pitcher,homerun2-schedule-pitcher`: their errors are failed workflows and
  findings). An alert is paired with the next non-alert message of the same system and title,
  ignoring prefixes like `[FIRING:1]` / `[RESOLVED]`; without one it is `ongoing`.

The pitched message gets the counts under scout's digest:

```
Git: 5 PRs merged, 2 opened, 2 releases, 14 commits, 1 failed workflow runs, 0 issues opened, 0 closed
Releases: homerun2-scout v0.12.0, homerun2-ai-pitcher v0.3.0
Outage: grafana - Vault unreachable (since 2026-10-14 10:00Z, 37 min)
```

An older scout answers 404: the run goes on, the prompt says the activity is unknown, and the
message says why. **Privacy:** with the free OpenRouter models a provider may log prompts. Only
titles go out (PRs, releases, issues, alerts), never message texts; narrow `MESSAGES_SYSTEMS`,
set `OUTAGES_ENABLED=false`, or exclude logging providers in the OpenRouter account if that is too
much.

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
| `MODE` | `morning` | `morning`, `evening`, `week-ahead`, `weekly`, `cluster-analysis` or `incident`, see [Modes](#modes); sets the defaults of `DIGEST_SCHEDULE`, `PITCH_SYSTEM`, `TICKER_ENABLED`, `MESSAGES_ENABLED`, `OUTAGES_ENABLED` |
| `DIGEST_SCHEDULE` | mode's | `hourly`, `daily` or `weekly` (scout v0.12+, needs scout retention ≥ 8 days, 15 for the week before): which scout window |
| `MESSAGES_ENABLED` | mode's | read the git activity from scout's `/analytics/messages` |
| `MESSAGES_SYSTEMS` | `homerun2-git-pitcher` | systems whose messages count as git activity |
| `OUTAGES_ENABLED` | mode's | pair error/critical messages into outages |
| `OUTAGE_EXCLUDE_SYSTEMS` | `homerun2-git-pitcher,homerun2-schedule-pitcher` | systems whose errors are not outages |
| `MAX_TITLES` | `5` | merged PR / opened issue titles per repository given to the model (`0` = counts only) |
| `SCHEDULE_PITCHER_URL` | (empty = skip) | schedule-pitcher base URL |
| `SCHEDULE_PITCHER_TOKEN` | | schedule-pitcher API bearer token |
| `MAX_FINDINGS` | `20` | findings passed to the model (worst first) |
| `OPENROUTER_API_KEY` | (empty = fallback) | OpenRouter API key |
| `OPENROUTER_MODEL` | `openrouter/free` | an OpenRouter model id, or a **comma-separated list** tried in order: OpenRouter's `models` fallback takes at most 3, so a longer list is sent in groups of 3, one after the other, e.g. `google/gemma-4-31b-it:free,nvidia/nemotron-3-super-120b-a12b:free,openrouter/free`. `openrouter/free` alone may route to very small models (2.6B) that misread findings |
| `OPENROUTER_BASE_URL` | `https://openrouter.ai/api/v1` | OpenAI-compatible endpoint |
| `LLM_TIMEOUT` | `30s` | timeout of **one request**, i.e. one model with the default `LLM_MODELS_PER_REQUEST=1`. Cut to a fair share of the run's remaining time, so the later models of the list still get their turn before the deadline. A free model that hangs costs this much, not the whole run |
| `LLM_MODELS_PER_REQUEST` | `1` | models of `OPENROUTER_MODEL` per request (1–3). With 2–3 OpenRouter falls back server-side inside the group, but one hanging model then uses the group's whole timeout |
| `LLM_DIAL_NETWORK` | `tcp` | `tcp`: all addresses of the host, IPv4 first, then IPv6; `tcp4` / `tcp6`: one family only. When every address fails, the error lists each address with its own cause (Go's dialer only reports the first, which hid IPv4 failures behind an IPv6 "network is unreachable", #11) |
| `LLM_MAX_TOKENS` | `4000` | token cap of the answer **including a reasoning model's thinking** (`reasoning.effort: low`, thinking excluded from the answer). An answer cut by the cap is trimmed to its last full sentence and marked |
| `SUMMARY_LANGUAGE` | `German` | language of the AI text |
| `OPENROUTER_MODEL_DENYLIST` | `safety,guard` | substrings of model ids whose answers are rejected (and retried): `openrouter/free` also routes to classifiers such as `nvidia/nemotron-3.5-content-safety`, which answers `User Safety: safe` |
| `SUMMARY_MIN_CHARS` | `80` | a shorter summary counts as a failed call (`0` = off) |
| `LLM_RETRIES` | `2` | more tries after a failed model call (HTTP 429/5xx, network, empty, cut-off, too short or denied answer; 400/401/402/403 are not retried); back-off `LLM_RETRY_DELAY` (`5s`), doubled each time. A missing key is not retried |
| `TICKER_ENABLED` | mode's | also pitch short ticker lines for the LED matrix |
| `OPENROUTER_TICKER_MODEL` | `OPENROUTER_MODEL` | model for the ticker lines |
| `TICKER_MAX` | `4` | at most this many ticker lines |
| `TICKER_MAX_CHARS` | `40` | at most this many characters per line |
| `TICKER_SYSTEM` | `ai-ticker` | `system` of the ticker messages |
| `TICKER_DELAY` | `3s` | pause before each ticker pitch, so the matrix shows them one by one |
| `PITCHER_URL` | (required unless dry run) | omni-pitcher base URL; `/pitch` is appended |
| `PITCHER_TOKEN` | (required unless dry run) | omni-pitcher bearer token |
| `PITCH_SYSTEM` | mode's | `system` of the pitched message |
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

## Image variant `-claude` (#13)

`MODE=cluster-analysis` runs the Claude Code CLI, which the default image
(`cgr.dev/chainguard/static`, ko) does not have. So every release also ships
`ghcr.io/stuttgart-things/homerun2-ai-pitcher:<version>-claude`: the same
binary, built by ko on `ghcr.io/stuttgart-things/homerun2-ai-pitcher-claude-base`
(`images/claude-code/Dockerfile`: Node 24 + `@anthropic-ai/claude-code`
pinned, auto-updater and telemetry off, `HOME=/tmp`, uid 1000).

- The base is built by `.github/workflows/build-claude-base.yaml` when its
  Dockerfile changes (Renovate bumps the CLI there), tagged with the CLI
  version. The release job `release-claude` pins it by that version.
- PRs build `pr-<n>-claude`, so a broken base fails before the merge.
- KCL: a run with `mode: cluster-analysis` gets `<image>-claude`, an
  `emptyDir` at `/tmp` (read-only root, `HOME=/tmp`), 40 min deadline and its
  Secrets via `envFromSecrets`; every other mode keeps the slim image.

About 900 MB (Node and the CLI), for a daily job.

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

Without `config.runs` it renders one CronJob (`config.mode`, default `morning`, at
`config.schedule`), as before. With `config.runs` it renders one CronJob per entry, all sharing
the Secret; a morning run keeps the plain name `homerun2-ai-pitcher`, the others get
`homerun2-ai-pitcher-<mode>` (or the entry's `name`):

```bash
kcl run kcl/main.k -D 'config.runs=[
  {"mode":"morning","schedule":"45 7 * * 1-5"},
  {"mode":"evening","schedule":"45 18 * * 1-5"},
  {"mode":"week-ahead","schedule":"50 7 * * 1"},
  {"mode":"weekly","schedule":"0 15 * * 5","extraEnvVars":{"TICKER_MAX":"3"}}]'
```

The image is built with [ko](https://ko.build) (`.ko.yaml`, no Dockerfile) and released by
semantic-release on conventional commits (`fix:`, `feat:`).

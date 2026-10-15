"""
CronJobs: one run, one pitched message, one CronJob per entry of runs (or one
for mode/schedule). Missing tokens are optional on purpose: without the
OpenRouter key or the schedule-pitcher token the run degrades (plain digest,
no findings) instead of failing to start.
"""

import labels

config = labels.config

_secretEnv = lambda envName: str, key: str, optional: bool -> {str:} {
    {
        name = envName
        valueFrom.secretKeyRef = {
            name = config.name
            key = key
            optional = optional
        }
    }
}

_env = [
    {name = "SCOUT_URL", value = config.scoutURL}
    _secretEnv("SCOUT_TOKEN", "scout-token", False)
    {name = "SCHEDULE_PITCHER_URL", value = config.schedulePitcherURL}
    _secretEnv("SCHEDULE_PITCHER_TOKEN", "schedule-pitcher-token", True)
    {name = "PITCHER_URL", value = config.pitcherURL}
    _secretEnv("PITCHER_TOKEN", "pitcher-token", False)
    _secretEnv("OPENROUTER_API_KEY", "openrouter-api-key", True)
    {name = "OPENROUTER_MODEL", value = config.model}
    {name = "SUMMARY_LANGUAGE", value = config.language}
    {name = "LLM_RETRIES", value = str(config.retries)}
] + ([{name = "DIGEST_SCHEDULE", value = config.digestSchedule}] if config.digestSchedule else []) + ([{name = "TICKER_ENABLED", value = "true" if config.tickerEnabled else "false"}] if config.tickerEnabled in [True, False] else []) + ([{name = "OPENROUTER_TICKER_MODEL", value = config.tickerModel}] if config.tickerModel else []) + [{name = k, value = v} for k, v in config.extraEnvVars]

# Without runs: one run of mode at schedule, as before runs existed.
_runs = [{
    name = r.name
    mode = r.mode
    schedule = r.schedule
    suspend = r.suspend
    extraEnvVars = r.extraEnvVars
} for r in config.runs] or [{
    name = ""
    mode = config.mode
    schedule = config.schedule
    suspend = config.suspend
    extraEnvVars = {}
}]

# morning keeps the plain name, so a consumer's patches on it still match.
_jobName = lambda run: {str:} -> str {
    run.name if run.name else (config.name if run.mode == "morning" else config.name + "-" + run.mode)
}

_cronJob = lambda run: {str:} -> {str:} {
    _name = _jobName(run)
    _labels = {
        **labels.commonLabels
        "app.kubernetes.io/instance" = _name
        "homerun2.stuttgart-things.com/ai-mode" = run.mode
    }
    {
        apiVersion = "batch/v1"
        kind = "CronJob"
        metadata = {
            name = _name
            namespace = config.namespace
            labels = _labels
        }
        spec = {
            schedule = run.schedule
            timeZone = config.timeZone
            suspend = run.suspend
            concurrencyPolicy = "Forbid"
            successfulJobsHistoryLimit = 3
            failedJobsHistoryLimit = 3
            jobTemplate.spec = {
                backoffLimit = 1
                # Above the run's own budget with the defaults (~12 min: two model
                # calls with 3 tries of 90 s each, back-off, ticker spacing).
                activeDeadlineSeconds = 1200
                template = {
                    metadata.labels = _labels
                    spec = {
                        restartPolicy = "Never"
                        automountServiceAccountToken = False
                        securityContext = {
                            runAsNonRoot = True
                            seccompProfile.type = "RuntimeDefault"
                        }
                        containers = [{
                            name = config.name
                            image = config.image
                            imagePullPolicy = config.imagePullPolicy
                            env = [{name = "MODE", value = run.mode}] + _env + [{name = k, value = v} for k, v in run.extraEnvVars]
                            resources = {
                                requests = {cpu = config.cpuRequest, memory = config.memoryRequest}
                                limits = {cpu = config.cpuLimit, memory = config.memoryLimit}
                            }
                            securityContext = {
                                allowPrivilegeEscalation = False
                                readOnlyRootFilesystem = True
                                capabilities.drop = ["ALL"]
                            }
                        }]
                    }
                }
            }
        }
    }
}

cronJobs = [_cronJob(r) for r in _runs]

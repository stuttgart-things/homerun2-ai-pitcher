"""
CronJob: one run, one pitched message. Missing tokens are optional on
purpose: without the OpenRouter key or the schedule-pitcher token the run
degrades (plain digest, no findings) instead of failing to start.
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
    {name = "DIGEST_SCHEDULE", value = config.digestSchedule}
] + [{name = k, value = v} for k, v in config.extraEnvVars]

cronJob = {
    apiVersion = "batch/v1"
    kind = "CronJob"
    metadata = {
        name = config.name
        namespace = config.namespace
        labels = labels.commonLabels
    }
    spec = {
        schedule = config.schedule
        timeZone = config.timeZone
        suspend = config.suspend
        concurrencyPolicy = "Forbid"
        successfulJobsHistoryLimit = 3
        failedJobsHistoryLimit = 3
        jobTemplate.spec = {
            backoffLimit = 1
            activeDeadlineSeconds = 600
            template = {
                metadata.labels = labels.commonLabels
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
                        env = _env
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

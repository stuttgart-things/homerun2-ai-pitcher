"""
MODE=incident (#5): a stream consumer, as a Deployment (default) or as a
KEDA ScaledJob. Emitted only with incidentEnabled.

The Deployment runs one replica: the correlation state lives in Redis, but
two consumers would race on the same fingerprint. The ScaledJob starts at
most one Job at a time for the same reason, and only when the consumer
group lags behind (lagCount, Redis >= 7): pendingEntriesCount would only
count entries a consumer already read, so new alerts would never start a
Job.
"""

import labels

config = labels.config

_name = config.name + "-incident"
_labels = {
    **labels.commonLabels
    "app.kubernetes.io/instance" = _name
    "app.kubernetes.io/component" = "ai-incident"
    "homerun2.stuttgart-things.com/ai-mode" = "incident"
}

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

_scaled = config.incidentRunAs == "scaledjob"

_env = [
    {name = "MODE", value = "incident"}
    {name = "INCIDENT_RUN", value = "once" if _scaled else "loop"}
    {name = "INCIDENT_STREAMS", value = ",".join(config.redisStreams)}
    {name = "REDIS_ADDR", value = config.redisAddr}
    {name = "REDIS_PORT", value = config.redisPort}
    _secretEnv("REDIS_PASSWORD", "redis-password", True)
    {name = "PITCHER_URL", value = config.pitcherURL}
    _secretEnv("PITCHER_TOKEN", "pitcher-token", False)
    _secretEnv("OPENROUTER_API_KEY", "openrouter-api-key", True)
    {name = "OPENROUTER_MODEL", value = config.model}
    {name = "SUMMARY_LANGUAGE", value = config.language}
    {name = "LLM_RETRIES", value = str(config.retries)}
    {name = "INCIDENT_TIMEZONE", value = config.timeZone}
] + [{name = k, value = v} for k, v in config.incidentExtraEnvVars]

_podSpec = {
    restartPolicy = "Never" if _scaled else "Always"
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

_deployment = {
    apiVersion = "apps/v1"
    kind = "Deployment"
    metadata = {
        name = _name
        namespace = config.namespace
        labels = _labels
    }
    spec = {
        replicas = 1
        # Never two consumers at once, not even during a rollout.
        strategy.type = "Recreate"
        selector.matchLabels = {
            "app.kubernetes.io/name" = config.name
            "app.kubernetes.io/instance" = _name
        }
        template = {
            metadata.labels = _labels
            spec = _podSpec
        }
    }
}

_triggerAuth = {
    apiVersion = "keda.sh/v1alpha1"
    kind = "TriggerAuthentication"
    metadata = {
        name = _name + "-redis"
        namespace = config.namespace
        labels = _labels
    }
    spec.secretTargetRef = [{
        parameter = "password"
        name = config.name
        key = "redis-password"
    }]
}

_scaledJob = {
    apiVersion = "keda.sh/v1alpha1"
    kind = "ScaledJob"
    metadata = {
        name = _name
        namespace = config.namespace
        labels = _labels
    }
    spec = {
        jobTargetRef = {
            # A Job drains the streams and waits for an open window to close
            # (INCIDENT_WINDOW 15m at most).
            activeDeadlineSeconds = 1500
            backoffLimit = 1
            template = {
                metadata.labels = _labels
                spec = _podSpec
            }
        }
        pollingInterval = 30
        maxReplicaCount = 1
        successfulJobsHistoryLimit = 3
        failedJobsHistoryLimit = 3
        triggers = [{
            type = "redis-streams"
            metadata = {
                address = config.redisAddr + ":" + config.redisPort
                stream = s
                consumerGroup = "homerun2-ai-pitcher-incident"
                lagCount = "1"
            }
            authenticationRef.name = _name + "-redis"
        } for s in config.redisStreams]
    }
}

manifests = ([_triggerAuth, _scaledJob] if _scaled else [_deployment]) if config.incidentEnabled else []

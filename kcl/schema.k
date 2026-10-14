"""
Schema for deploying homerun2-ai-pitcher: one CronJob that summarises scout's
digest and the schedule-pitcher's open findings and pitches the result.
"""

schema AiPitcher:
    """Configuration for a homerun2-ai-pitcher deployment"""

    name: str = "homerun2-ai-pitcher"
    namespace: str = "homerun2"

    image: str = "ghcr.io/stuttgart-things/homerun2-ai-pitcher:main"
    imagePullPolicy: str = "IfNotPresent"

    # 07:45 Berlin: after scout's own 07:00 digest
    schedule: str = "45 7 * * *"
    timeZone: str = "Europe/Berlin"
    suspend: bool = False

    cpuRequest: str = "10m"
    cpuLimit: str = "200m"
    memoryRequest: str = "32Mi"
    memoryLimit: str = "128Mi"

    # Inputs and output, in-cluster service URLs
    scoutURL: str = "http://homerun2-scout.homerun2.svc.cluster.local"
    schedulePitcherURL: str = "http://homerun2-schedule-pitcher.homerun2.svc.cluster.local"
    pitcherURL: str = "http://homerun2-omni-pitcher.homerun2.svc.cluster.local"

    # OpenRouter
    model: str = "openrouter/free"
    language: str = "German"
    digestSchedule: "daily" | "hourly" = "daily"
    # More tries after a failed model call (free models fail often)
    retries: int = 2
    # Short ASCII lines for the LED matrix, one message each (system ai-ticker)
    tickerEnabled: bool = True
    # Model for the tickers; empty = the same as model
    tickerModel: str = ""

    # Secret <name> (keys scout-token, schedule-pitcher-token, pitcher-token,
    # openrouter-api-key), emitted only when a value is given. In GitOps the
    # values come from SOPS, ESO or a Flux substitution instead.
    scoutToken: str = ""
    schedulePitcherToken: str = ""
    pitcherToken: str = ""
    openrouterAPIKey: str = ""

    extraEnvVars: {str:str} = {}
    labels: {str:str} = {}

    check:
        name != "", "name must not be empty"
        namespace != "", "namespace must not be empty"
        schedule != "", "schedule must not be empty"
        retries >= 0, "retries must be >= 0"

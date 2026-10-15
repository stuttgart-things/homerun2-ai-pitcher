"""
Schema for deploying homerun2-ai-pitcher: CronJobs that summarise scout's
digest, its messages and the schedule-pitcher's open findings and pitch the
result. One CronJob per entry of runs, or one for mode/schedule without runs.
"""

schema Run:
    """One scheduled run: its own CronJob with its own MODE"""
    # CronJob name; empty = <name> for morning, <name>-<mode> otherwise
    name: str = ""
    mode: "morning" | "evening" | "week-ahead" | "weekly"
    # Cron in timeZone
    schedule: str
    suspend: bool = False
    extraEnvVars: {str:str} = {}

    check:
        schedule != "", "run schedule must not be empty"

schema AiPitcher:
    """Configuration for a homerun2-ai-pitcher deployment"""
    name: str = "homerun2-ai-pitcher"
    namespace: str = "homerun2"

    image: str = "ghcr.io/stuttgart-things/homerun2-ai-pitcher:main"
    imagePullPolicy: str = "IfNotPresent"

    # Without runs: one CronJob, MODE mode, at schedule. 07:45 Berlin: after
    # scout's own 07:00 digest.
    mode: "morning" | "evening" | "week-ahead" | "weekly" = "morning"
    schedule: str = "45 7 * * *"

    # With runs: one CronJob per entry instead, e.g.
    #   [{mode = "morning", schedule = "45 7 * * 1-5"},
    #    {mode = "evening", schedule = "45 18 * * 1-5"},
    #    {mode = "week-ahead", schedule = "50 7 * * 1"},
    #    {mode = "weekly", schedule = "0 15 * * 5"}]
    runs: [Run] = []
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
    # Empty = the mode's window (weekly for weekly, else daily)
    digestSchedule: "" | "daily" | "hourly" | "weekly" = ""
    # More tries after a failed model call (free models fail often)
    retries: int = 2

    # Short ASCII lines for the LED matrix, one message each (system
    # ai-ticker). Unset = the mode's default (morning and weekly: on)
    tickerEnabled?: bool
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


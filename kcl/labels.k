"""
Configuration from options and common labels
"""

import schema

_opts = {
    name = option("config.name")
    namespace = option("config.namespace")
    image = option("config.image")
    schedule = option("config.schedule")
    timeZone = option("config.timeZone")
    suspend = option("config.suspend")
    scoutURL = option("config.scoutURL")
    schedulePitcherURL = option("config.schedulePitcherURL")
    pitcherURL = option("config.pitcherURL")
    model = option("config.model")
    language = option("config.language")
    digestSchedule = option("config.digestSchedule")
    retries = option("config.retries")
    tickerEnabled = option("config.tickerEnabled")
    tickerModel = option("config.tickerModel")
    scoutToken = option("config.scoutToken")
    schedulePitcherToken = option("config.schedulePitcherToken")
    pitcherToken = option("config.pitcherToken")
    openrouterAPIKey = option("config.openrouterAPIKey")
    extraEnvVars = option("config.extraEnvVars")
}

config: schema.AiPitcher = schema.AiPitcher {
    if _opts.name:
        name = _opts.name
    if _opts.namespace:
        namespace = _opts.namespace
    if _opts.image:
        image = _opts.image
    if _opts.schedule:
        schedule = _opts.schedule
    if _opts.timeZone:
        timeZone = _opts.timeZone
    if _opts.suspend:
        suspend = _opts.suspend in [True, "True", "true"]
    if _opts.scoutURL:
        scoutURL = _opts.scoutURL
    if _opts.schedulePitcherURL:
        schedulePitcherURL = _opts.schedulePitcherURL
    if _opts.pitcherURL:
        pitcherURL = _opts.pitcherURL
    if _opts.model:
        model = _opts.model
    if _opts.language:
        language = _opts.language
    if _opts.digestSchedule:
        digestSchedule = _opts.digestSchedule
    if _opts.retries != None:
        retries = int(_opts.retries)
    if _opts.tickerEnabled != None:
        tickerEnabled = _opts.tickerEnabled in [True, "True", "true"]
    if _opts.tickerModel:
        tickerModel = _opts.tickerModel
    if _opts.scoutToken:
        scoutToken = _opts.scoutToken
    if _opts.schedulePitcherToken:
        schedulePitcherToken = _opts.schedulePitcherToken
    if _opts.pitcherToken:
        pitcherToken = _opts.pitcherToken
    if _opts.openrouterAPIKey:
        openrouterAPIKey = _opts.openrouterAPIKey
    if _opts.extraEnvVars:
        extraEnvVars = _opts.extraEnvVars
}

commonLabels = {
    "app.kubernetes.io/name": config.name
    "app.kubernetes.io/instance": config.name
    "app.kubernetes.io/component": "ai-digest"
    "app.kubernetes.io/part-of": "homerun2"
    **config.labels
}

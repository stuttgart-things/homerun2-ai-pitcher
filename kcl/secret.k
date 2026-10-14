"""
The Secret the CronJob reads, emitted only when at least one value is given.
"""

import base64
import labels

config = labels.config

_data = {
    k: base64.encode(v) for k, v in {
        "scout-token": config.scoutToken
        "schedule-pitcher-token": config.schedulePitcherToken
        "pitcher-token": config.pitcherToken
        "openrouter-api-key": config.openrouterAPIKey
    } if v
}

secret = {
    apiVersion = "v1"
    kind = "Secret"
    metadata = {
        name = config.name
        namespace = config.namespace
        labels = labels.commonLabels
    }
    type = "Opaque"
    data = _data
} if _data else None

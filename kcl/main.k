"""
homerun2-ai-pitcher: CronJob(s) plus their Secret.
Namespaces are not emitted: the consuming Application creates them, like for
the sibling services.
"""

import secret
import cronjob

manifests = [m for m in [secret.secret] if m] + cronjob.cronJobs

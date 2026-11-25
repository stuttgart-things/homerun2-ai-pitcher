package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
	_ "time/tzdata" // the image has no zoneinfo; INCIDENT_TIMEZONE needs it
)

// IncidentMode is the hint/incident mode of #5: a stream consumer, not a
// CronJob run.
const IncidentMode = "incident"

// Incident configures MODE=incident.
type Incident struct {
	RedisAddr     string
	RedisPassword string
	Streams       []string
	Group         string
	Consumer      string
	StatePrefix   string
	// Once: drain the streams, close the open window, exit (a Job, e.g.
	// KEDA's ScaledJob). Otherwise loop (a Deployment).
	Once      bool
	ClaimIdle time.Duration

	ExcludeSystems []string
	Severities     []string
	MinOccurrences int
	Cooldown       time.Duration
	StaleAfter     time.Duration
	Retention      time.Duration

	Window    time.Duration
	Quiet     time.Duration
	MinAlerts int
	AI        bool
	Location  *time.Location
	Batch     int
	Block     time.Duration
}

func loadIncident(c *Config) error {
	i := &c.Incident
	host := getEnv("REDIS_ADDR", "")
	if host == "" {
		return fmt.Errorf("MODE=incident needs REDIS_ADDR")
	}
	i.RedisAddr = host + ":" + getEnv("REDIS_PORT", "6379")
	i.RedisPassword = getEnv("REDIS_PASSWORD", "")
	i.Streams = splitList(getEnv("INCIDENT_STREAMS", "alerts,messages"))
	i.Group = getEnv("INCIDENT_GROUP", "homerun2-ai-pitcher-incident")
	hn, _ := os.Hostname()
	i.Consumer = getEnv("INCIDENT_CONSUMER", hn)
	if i.Consumer == "" {
		i.Consumer = "homerun2-ai-pitcher"
	}
	i.StatePrefix = getEnv("INCIDENT_STATE_PREFIX", "homerun2-ai-pitcher:incident")
	switch run := getEnv("INCIDENT_RUN", "loop"); run {
	case "loop":
	case "once":
		i.Once = true
	default:
		return fmt.Errorf("INCIDENT_RUN must be loop or once, got %q", run)
	}
	// Every ai-* system, so the mode never reads its own output nor the
	// digests', and scout's digest (#135: never fed its own output).
	i.ExcludeSystems = splitList(getEnv("HINT_EXCLUDE_SYSTEMS", "ai-*,scout-digest"))
	i.Severities = splitList(getEnv("HINT_SEVERITIES", "error,critical"))
	i.AI = getEnv("INCIDENT_AI", "true") == "true"

	ints := []struct {
		env, def string
		min      int
		dst      *int
	}{
		{"INCIDENT_MIN_OCCURRENCES", "2", 2, &i.MinOccurrences},
		{"INCIDENT_MIN_ALERTS", "2", 2, &i.MinAlerts},
		{"INCIDENT_BATCH", "50", 1, &i.Batch},
	}
	for _, x := range ints {
		v, err := strconv.Atoi(getEnv(x.env, x.def))
		if err != nil || v < x.min {
			return fmt.Errorf("invalid %s %q (want an integer >= %d)", x.env, os.Getenv(x.env), x.min)
		}
		*x.dst = v
	}
	durs := []struct {
		env, def string
		dst      *time.Duration
	}{
		{"HINT_COOLDOWN", "6h", &i.Cooldown},
		{"INCIDENT_STALE_AFTER", "26h", &i.StaleAfter},
		{"INCIDENT_RETENTION", "168h", &i.Retention},
		{"INCIDENT_WINDOW", "15m", &i.Window},
		{"INCIDENT_QUIET", "5m", &i.Quiet},
		{"INCIDENT_CLAIM_IDLE", "5m", &i.ClaimIdle},
		{"INCIDENT_BLOCK", "5s", &i.Block},
	}
	for _, x := range durs {
		v, err := time.ParseDuration(getEnv(x.env, x.def))
		if err != nil || v <= 0 {
			return fmt.Errorf("invalid %s %q", x.env, os.Getenv(x.env))
		}
		*x.dst = v
	}
	loc, err := time.LoadLocation(getEnv("INCIDENT_TIMEZONE", "Europe/Berlin"))
	if err != nil {
		return fmt.Errorf("invalid INCIDENT_TIMEZONE: %w", err)
	}
	i.Location = loc

	if !c.DryRun && (c.PitcherURL == "" || c.PitcherToken == "") {
		return fmt.Errorf("missing required settings: PITCHER_URL, PITCHER_TOKEN")
	}
	return nil
}

package config

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/stuttgart-things/homerun2-ai-pitcher/internal/clusteranalysis"
)

// ClusterAnalysisMode is the mode of homerun2-ai-pitcher#12: Claude Code over
// the K8sGPT findings archived in S3. It does not read scout.
const ClusterAnalysisMode = "cluster-analysis"

// Analysis configures MODE=cluster-analysis.
type Analysis struct {
	// S3 (homerun2-s3-catcher's MinIO): one endpoint, two buckets, each with
	// its own user -- read-only on the archive, writer on the reports.
	Endpoint       string
	Region         string
	UseSSL         bool
	Insecure       bool
	ArchiveBucket  string
	ArchiveKey     string
	ArchiveSecret  string
	ReportBucket   string
	ReportKey      string
	ReportSecret   string
	Sources        []clusteranalysis.Source
	LookbackDays   int
	StaleAfter     time.Duration
	MaxPerCluster  int
	ReportURL      string // link in the pitch, e.g. the portal's /archive/?tab=analyses
	ClaudeBin      string
	ClaudeModel    string
	ClaudeBudget   float64
	ClaudeTimeout  time.Duration
	ClaudeAttempts int
}

func loadAnalysis(c *Config) error {
	a := &c.Analysis
	a.Endpoint = getEnv("S3_ENDPOINT", "")
	a.Region = getEnv("S3_REGION", "us-east-1")
	a.UseSSL = getEnv("S3_USE_SSL", "true") == "true"
	a.Insecure = getEnv("S3_INSECURE", "false") == "true"
	a.ArchiveBucket = getEnv("ARCHIVE_BUCKET", "k8s-cluster-report")
	a.ArchiveKey = getEnv("ARCHIVE_ACCESS_KEY", "")
	a.ArchiveSecret = getEnv("ARCHIVE_SECRET_KEY", "")
	a.ReportBucket = getEnv("REPORT_BUCKET", "k8s-cluster-analysis")
	a.ReportKey = getEnv("REPORT_ACCESS_KEY", "")
	a.ReportSecret = getEnv("REPORT_SECRET_KEY", "")
	var err error
	if a.Sources, err = clusteranalysis.ParseSources(getEnv("ANALYSIS_SOURCES", "k8sgpt,endpoints,vm")); err != nil {
		return fmt.Errorf("invalid ANALYSIS_SOURCES: %w", err)
	}
	a.ReportURL = getEnv("REPORT_URL", "")
	a.ClaudeBin = getEnv("CLAUDE_BIN", "claude")
	a.ClaudeModel = getEnv("CLAUDE_MODEL", "sonnet")

	for _, i := range []struct {
		env      string
		def, min int
		dst      *int
	}{
		{"LOOKBACK_DAYS", 7, 1, &a.LookbackDays},
		{"MAX_FINDINGS_PER_CLUSTER", 50, 1, &a.MaxPerCluster},
		{"CLAUDE_ATTEMPTS", 2, 1, &a.ClaudeAttempts},
	} {
		if *i.dst, err = strconv.Atoi(getEnv(i.env, strconv.Itoa(i.def))); err != nil || *i.dst < i.min {
			return fmt.Errorf("invalid %s (want an integer >= %d)", i.env, i.min)
		}
	}
	for _, d := range []struct {
		env, def string
		dst      *time.Duration
	}{
		{"STALE_AFTER", "3h", &a.StaleAfter},
		{"CLAUDE_TIMEOUT", "10m", &a.ClaudeTimeout},
	} {
		if *d.dst, err = time.ParseDuration(getEnv(d.env, d.def)); err != nil || *d.dst <= 0 {
			return fmt.Errorf("invalid %s", d.env)
		}
	}
	if a.ClaudeBudget, err = strconv.ParseFloat(getEnv("CLAUDE_MAX_BUDGET_USD", "2"), 64); err != nil || a.ClaudeBudget < 0 {
		return fmt.Errorf("invalid CLAUDE_MAX_BUDGET_USD")
	}

	var missing []string
	for env, v := range map[string]string{
		"S3_ENDPOINT": a.Endpoint, "ARCHIVE_ACCESS_KEY": a.ArchiveKey, "ARCHIVE_SECRET_KEY": a.ArchiveSecret,
		"REPORT_ACCESS_KEY": a.ReportKey, "REPORT_SECRET_KEY": a.ReportSecret,
	} {
		if v == "" {
			missing = append(missing, env)
		}
	}
	if !c.DryRun {
		if c.PitcherURL == "" {
			missing = append(missing, "PITCHER_URL")
		}
		if c.PitcherToken == "" {
			missing = append(missing, "PITCHER_TOKEN")
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return errors.New("missing required settings: " + strings.Join(missing, ", "))
	}
	return nil
}

package config

import (
	"strings"
	"testing"
)

func TestClusterAnalysisConfig(t *testing.T) {
	t.Setenv("MODE", "cluster-analysis")
	t.Setenv("DRY_RUN", "true")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ARCHIVE_ACCESS_KEY") || !strings.Contains(err.Error(), "S3_ENDPOINT") {
		t.Errorf("missing settings not reported: %v", err)
	}
	for k, v := range map[string]string{"S3_ENDPOINT": "minio:9000", "ARCHIVE_ACCESS_KEY": "a", "ARCHIVE_SECRET_KEY": "b", "REPORT_ACCESS_KEY": "c", "REPORT_SECRET_KEY": "d"} {
		t.Setenv(k, v)
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	a := c.Analysis
	if c.System != "ai-cluster-analysis" || a.ArchiveBucket != "k8s-cluster-report" || a.ReportBucket != "k8s-cluster-analysis" ||
		a.LookbackDays != 7 || a.ClaudeModel != "sonnet" || a.ClaudeBudget != 2 || a.ClaudeAttempts != 2 || !a.UseSSL {
		t.Errorf("defaults %+v", a)
	}
	// No scout needed in this mode.
	if c.ScoutURL != "" {
		t.Errorf("scout url %q", c.ScoutURL)
	}
	if a.Labs != nil {
		t.Errorf("labs without ANALYSIS_LABS: %+v", a.Labs)
	}
	t.Setenv("ANALYSIS_LABS", "LabUL=platform-sthings,*-labul;LabDA=sthings-app*")
	if c, err = Load(); err != nil || c.Analysis.Labs.Of("sbx-labul") != "LabUL" {
		t.Errorf("ANALYSIS_LABS: %v", err)
	}
	t.Setenv("ANALYSIS_LABS", "LabUL")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "ANALYSIS_LABS") {
		t.Errorf("invalid ANALYSIS_LABS accepted: %v", err)
	}
	t.Setenv("ANALYSIS_LABS", "")
	t.Setenv("LOOKBACK_DAYS", "0")
	if _, err := Load(); err == nil {
		t.Error("LOOKBACK_DAYS=0 accepted")
	}
}

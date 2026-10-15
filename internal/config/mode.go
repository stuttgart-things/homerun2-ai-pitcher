package config

import (
	"slices"
	"strings"
)

// Mode is what a run summarises and for when. Its fields are the defaults
// of DIGEST_SCHEDULE, PITCH_SYSTEM, TICKER_ENABLED, MESSAGES_ENABLED and
// OUTAGES_ENABLED; an env var that is set still wins.
type Mode struct {
	Name string
	// Label goes into the title ("[AI evening] ..."); empty for morning,
	// whose title stays "[AI] ..." as before modes existed.
	Label          string
	DigestSchedule string
	System         string
	Ticker         bool
	// Activity reads scout's messages: git activity and outages.
	Activity bool
}

// Modes are the runs a deployment schedules, each as its own CronJob
// (stuttgart-things/homerun2-ai-pitcher#6).
var Modes = map[string]Mode{
	// 07:45: what needs attention today, what happened overnight.
	"morning": {Name: "morning", DigestSchedule: "daily", System: "ai-digest", Ticker: true, Activity: true},
	// 18:45, before quiet hours: what stays open, what is due tomorrow.
	"evening": {Name: "evening", Label: "evening", DigestSchedule: "daily", System: "ai-evening", Activity: true},
	// Monday morning: findings due in the next seven days, by date.
	"week-ahead": {Name: "week-ahead", Label: "week ahead", DigestSchedule: "daily", System: "ai-week-ahead"},
	// Friday afternoon: the week's releases, PRs and outages, against the week before.
	"weekly": {Name: "weekly", Label: "weekly", DigestSchedule: "weekly", System: "ai-weekly", Ticker: true, Activity: true},
}

// ModeNames lists the modes, sorted.
func ModeNames() string {
	var names []string
	for n := range Modes {
		names = append(names, n)
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

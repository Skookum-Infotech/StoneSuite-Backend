package usage

import (
	"fmt"
	"math"
)

// Rule keys of the watch-list.
const (
	RuleSpike         = "spike"
	RuleRapidGrowth   = "rapid_growth"
	RuleFleetOutlier  = "fleet_outlier"
	RuleErrorRate     = "error_rate"
	RuleFailedJobs    = "failed_jobs"
	RuleDormant       = "dormant"
	RuleQuotaNear     = "quota_near"
	RuleQuotaExceeded = "quota_exceeded"
)

// Card keys flagged on the frontend usage cards (AlertFiring.MetricKey).
const (
	CardCompute   = "compute"
	CardDB        = "db"
	CardR2        = "r2"
	CardAITokens  = "ai_tokens"
	CardUsers     = "users"
	CardRequests  = "requests"
	CardErrorRate = "error_rate"
	CardJobs      = "jobs"
)

// Rule thresholds.
const (
	spikeBaselineDays   = 28
	spikeMedianDays     = 7
	spikeMinPriorDays   = 7
	spikeSigmaMultiple  = 3.0
	spikeMedianMultiple = 2.0

	growthLookbackDays = 7
	growthMinPct       = 50.0
	growthMinBytes     = 1 << 30

	errorRateMinRequests = 500
	errorRateThreshold   = 0.05

	failedJobsMinFailed = 3
	failedJobsMinDead   = 1

	dormantAfterDays = 14

	quotaNearRatio     = 0.8
	quotaExceededRatio = 1.0

	fleetPercentile = 95
	percentDivisor  = 100
)

// RuleInput is everything the watch-list rules need for one tenant; Evaluate reads nothing else.
type RuleInput struct {
	TenantID string
	Active   bool
	// Series maps metric key to daily values, oldest first; the last element is the rolled-up day.
	Series                 map[string][]float64
	DaysSinceSignIn        *int
	Requests24h            int64
	Errors5xx24h           int64
	FailedJobs24h          int64
	DeadJobs24h            int64
	ComputePerActiveUser   float64
	FleetP95ComputePerUser float64
	// Limits maps card key to a plan limit; a rule fires only for keys present with a positive limit.
	Limits map[string]float64
}

// quotaCards is the fixed evaluation order of quota rules.
var quotaCards = []string{CardCompute, CardDB, CardR2, CardAITokens, CardRequests, CardUsers}

// cardLabels names a card in alert messages.
var cardLabels = map[string]string{
	CardCompute:  "Compute",
	CardDB:       "Database storage",
	CardR2:       "R2 storage",
	CardAITokens: "AI tokens",
	CardRequests: "Requests",
	CardUsers:    "Active users",
}

// Evaluate runs every watch-list rule against one tenant and returns firings in a fixed order.
func Evaluate(in RuleInput) []AlertFiring {
	var out []AlertFiring
	out = append(out, quotaFirings(in)...)
	for _, card := range []string{CardCompute, CardRequests, CardAITokens} {
		if f, ok := spikeFiring(in, card); ok {
			out = append(out, f)
		}
	}
	for _, card := range []string{CardDB, CardR2} {
		if f, ok := growthFiring(in, card); ok {
			out = append(out, f)
		}
	}
	if f, ok := outlierFiring(in); ok {
		out = append(out, f)
	}
	if f, ok := errorRateFiring(in); ok {
		out = append(out, f)
	}
	if f, ok := failedJobsFiring(in); ok {
		out = append(out, f)
	}
	if f, ok := dormantFiring(in); ok {
		out = append(out, f)
	}
	return out
}

// quotaFirings emits quota_near / quota_exceeded for every card that has a plan limit.
func quotaFirings(in RuleInput) []AlertFiring {
	var out []AlertFiring
	for _, card := range quotaCards {
		limit, ok := in.Limits[card]
		if !ok || limit <= 0 {
			continue
		}
		used := lastValue(cardSeries(in, card))
		ratio := used / limit
		rule, sev := "", ""
		switch {
		case ratio >= quotaExceededRatio:
			rule, sev = RuleQuotaExceeded, SeverityCritical
		case ratio >= quotaNearRatio:
			rule, sev = RuleQuotaNear, SeverityWarning
		default:
			continue
		}
		lim := limit
		out = append(out, AlertFiring{
			TenantID: in.TenantID, RuleKey: rule, MetricKey: card, Severity: sev,
			Message: fmt.Sprintf("%s at %.0f%% of plan limit (%s of %s)", cardLabels[card],
				ratio*percentDivisor, formatCardValue(card, used), formatCardValue(card, limit)),
			Observed: used, Baseline: &lim,
		})
	}
	return out
}

// spikeFiring fires when the latest day exceeds mean+3σ of the prior 28 days and 2x the prior 7-day median.
func spikeFiring(in RuleInput, card string) (AlertFiring, bool) {
	s := cardSeries(in, card)
	if len(s) < spikeMinPriorDays+1 {
		return AlertFiring{}, false
	}
	last := s[len(s)-1]
	prior := s[:len(s)-1]
	window := prior[max(0, len(prior)-spikeBaselineDays):]
	mean, sd := meanStd(window)
	recent := prior[max(0, len(prior)-spikeMedianDays):]
	med := median(recent)
	if mean <= 0 || med <= 0 {
		return AlertFiring{}, false
	}
	if last <= mean+spikeSigmaMultiple*sd || last <= spikeMedianMultiple*med {
		return AlertFiring{}, false
	}
	base := mean
	return AlertFiring{
		TenantID: in.TenantID, RuleKey: RuleSpike, MetricKey: card, Severity: SeverityWarning,
		Message: fmt.Sprintf("%s spiked to %s in a day (28-day average %s)", cardLabels[card],
			formatCardValue(card, last), formatCardValue(card, mean)),
		Observed: last, Baseline: &base,
	}, true
}

// growthFiring fires when storage is up 50% or more over 7 days and by at least 1 GiB.
func growthFiring(in RuleInput, card string) (AlertFiring, bool) {
	s := cardSeries(in, card)
	if len(s) < growthLookbackDays+1 {
		return AlertFiring{}, false
	}
	last, prev := s[len(s)-1], s[len(s)-1-growthLookbackDays]
	if prev <= 0 || last-prev < growthMinBytes {
		return AlertFiring{}, false
	}
	pct := (last/prev - 1) * percentDivisor
	if pct < growthMinPct {
		return AlertFiring{}, false
	}
	base := prev
	return AlertFiring{
		TenantID: in.TenantID, RuleKey: RuleRapidGrowth, MetricKey: card, Severity: SeverityWarning,
		Message: fmt.Sprintf("%s up %.0f%% in %d days (%s → %s)", cardLabels[card], math.Round(pct),
			growthLookbackDays, formatBytesGB(prev), formatBytesGB(last)),
		Observed: last, Baseline: &base,
	}, true
}

// outlierFiring fires when compute per active user exceeds the fleet's 95th percentile.
func outlierFiring(in RuleInput) (AlertFiring, bool) {
	if lastValue(in.Series[MetricActiveUsers]) < 1 || in.FleetP95ComputePerUser <= 0 ||
		in.ComputePerActiveUser <= in.FleetP95ComputePerUser {
		return AlertFiring{}, false
	}
	base := in.FleetP95ComputePerUser
	return AlertFiring{
		TenantID: in.TenantID, RuleKey: RuleFleetOutlier, MetricKey: CardCompute, Severity: SeverityWarning,
		Message: fmt.Sprintf("Compute per active user is above the fleet's 95th percentile (%s vs %s)",
			formatHours(in.ComputePerActiveUser), formatHours(in.FleetP95ComputePerUser)),
		Observed: in.ComputePerActiveUser, Baseline: &base,
	}, true
}

// errorRateFiring fires when 5xx responses exceed 5% of 24h requests, given enough traffic.
func errorRateFiring(in RuleInput) (AlertFiring, bool) {
	if in.Requests24h < errorRateMinRequests {
		return AlertFiring{}, false
	}
	rate := float64(in.Errors5xx24h) / float64(in.Requests24h)
	if rate <= errorRateThreshold {
		return AlertFiring{}, false
	}
	pct := rate * percentDivisor
	return AlertFiring{
		TenantID: in.TenantID, RuleKey: RuleErrorRate, MetricKey: CardErrorRate, Severity: SeverityCritical,
		Message:  fmt.Sprintf("Server error rate %s over the last 24 hours", formatPercentOne(pct)),
		Detail:   fmt.Sprintf("%s requests, %s failed.", formatCount(in.Requests24h), formatCount(in.Errors5xx24h)),
		Observed: pct,
	}, true
}

// failedJobsFiring fires on any dead job or on 3 or more failed jobs in 24 hours.
func failedJobsFiring(in RuleInput) (AlertFiring, bool) {
	dead, failed := in.DeadJobs24h, in.FailedJobs24h
	if dead < failedJobsMinDead && failed < failedJobsMinFailed {
		return AlertFiring{}, false
	}
	f := AlertFiring{TenantID: in.TenantID, RuleKey: RuleFailedJobs, MetricKey: CardJobs, Severity: SeverityWarning}
	if dead >= failedJobsMinDead {
		f.Message = fmt.Sprintf("%s dead background %s in the last 24 hours", formatCount(dead), pluralize(dead, "job", "jobs"))
		f.Observed = float64(dead)
		if failed > 0 {
			f.Detail = fmt.Sprintf("%s failed %s in the same period.", formatCount(failed), pluralize(failed, "job", "jobs"))
		}
		return f, true
	}
	f.Message = fmt.Sprintf("%s failed background jobs in the last 24 hours", formatCount(failed))
	f.Observed = float64(failed)
	return f, true
}

// dormantFiring fires for an active tenant with no sign-ins for 14 days (or ever).
func dormantFiring(in RuleInput) (AlertFiring, bool) {
	if !in.Active {
		return AlertFiring{}, false
	}
	f := AlertFiring{TenantID: in.TenantID, RuleKey: RuleDormant, MetricKey: CardUsers, Severity: SeverityInfo}
	if in.DaysSinceSignIn == nil {
		f.Message = "No sign-ins yet"
		return f, true
	}
	if *in.DaysSinceSignIn < dormantAfterDays {
		return AlertFiring{}, false
	}
	f.Message = fmt.Sprintf("No sign-ins for %d days", *in.DaysSinceSignIn)
	f.Observed = float64(*in.DaysSinceSignIn)
	return f, true
}

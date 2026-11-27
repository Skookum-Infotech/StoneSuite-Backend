package usage

import (
	"context"
	"time"

	"stonesuite-backend/tenancy"
)

// computePerUser is compute hours (requests plus jobs) per active user, 0 with no active users.
func computePerUser(values map[string]float64, activeUsers int) float64 {
	if activeUsers < 1 {
		return 0
	}
	return (values[MetricComputeRequestHours] + values[MetricComputeJobHours]) / float64(activeUsers)
}

// fleetP95 is the 95th percentile of compute per active user over tenants with at least one active user.
func (r *Rollup) fleetP95(results map[string]tenantRollup) float64 {
	var per []float64
	for _, res := range results {
		if res.signIn.ActiveUsers >= 1 {
			per = append(per, computePerUser(res.values, res.signIn.ActiveUsers))
		}
	}
	return FleetP95(per)
}

// evaluate is pass 2 for one tenant: build the rule input, record firings, resolve quiet alerts.
func (r *Rollup) evaluate(ctx context.Context, res tenantRollup, day time.Time, fleetP95 float64) error {
	t := res.tenant
	from := day.AddDate(0, 0, -(rollupSeriesDays - 1))
	ids := []string{t.ID}
	daily, err := r.store.DailyValues(ctx, ids, from, day)
	if err != nil {
		return err
	}
	tokens, err := r.store.TokensByDay(ctx, ids, from, day)
	if err != nil {
		return err
	}
	series := map[string][]float64{}
	for _, v := range append(daily, tokens...) {
		idx := int(v.Day.UTC().Sub(from).Hours() / hoursPerDay)
		if idx < 0 || idx >= rollupSeriesDays || v.TenantID != t.ID {
			continue
		}
		if series[v.Key] == nil {
			series[v.Key] = make([]float64, rollupSeriesDays)
		}
		series[v.Key][idx] = v.Value
	}
	in := RuleInput{
		TenantID:               t.ID,
		Active:                 t.Status == tenancy.StatusActive,
		Series:                 series,
		DaysSinceSignIn:        daysSince(res.signIn.LastSignInAt, day.AddDate(0, 0, 1)),
		Requests24h:            int64(res.values[MetricRequests]),
		Errors5xx24h:           int64(res.values[MetricErrors5xx]),
		FailedJobs24h:          int64(res.values[MetricFailedJobs]),
		DeadJobs24h:            int64(res.values[MetricDeadJobs]),
		ComputePerActiveUser:   computePerUser(res.values, res.signIn.ActiveUsers),
		FleetP95ComputePerUser: fleetP95,
	}
	now := r.now()
	for _, f := range Evaluate(in) {
		if err := r.store.RecordFiring(ctx, f, now); err != nil {
			return err
		}
	}
	quietSince := day.AddDate(0, 0, -rollupQuietDays)
	if _, err := r.store.ResolveQuiet(ctx, t.ID, quietSince, now); err != nil {
		return err
	}
	return nil
}

// daysSince returns whole days from last to ref, nil when last is nil, never negative.
func daysSince(last *time.Time, ref time.Time) *int {
	if last == nil {
		return nil
	}
	d := max(0, int(ref.Sub(*last).Hours()/hoursPerDay))
	return &d
}

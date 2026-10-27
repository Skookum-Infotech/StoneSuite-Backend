package automation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/anomaly"
)

// anomalyScanJobName identifies the anomaly scan job in logs.
const anomalyScanJobName = "anomaly_scan"

// AnomalyScan runs the anomaly rules against the tenant database and announces
// each new finding to the people who can act on it (see package anomaly).
type AnomalyScan struct {
	deps Deps
}

// Name identifies the job in logs.
func (*AnomalyScan) Name() string { return anomalyScanJobName }

// Run does nothing while the workspace has switched the scan off (rule
// anomaly_scan). Otherwise it scans as the system actor, then announces
// pending findings. A scan error does not skip the alert pass, so findings stored
// by an earlier tick still go out; both errors are returned together.
func (j *AnomalyScan) Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	ctx = WithSystemActor(ctx)
	if _, on, err := loadRule(ctx, pool, RuleKeyAnomalyScan); err != nil {
		return Result{}, fmt.Errorf("anomaly rule: %w", err)
	} else if !on {
		slog.InfoContext(ctx, "anomaly scan is switched off for this workspace", "tenant_id", env.Tenant.ID)
		return Result{}, nil
	}
	var errs []error

	scan, err := anomaly.Scan(ctx, pool, anomaly.DefaultRules(), env.Today)
	if err != nil {
		errs = append(errs, fmt.Errorf("anomaly scan: %w", err))
	}
	res := Result{Transitioned: scan.Raised, Skipped: scan.Existing}

	if j.deps.Send == nil {
		slog.WarnContext(ctx, "anomaly alerts skipped: no notification sender configured", "tenant_id", env.Tenant.ID)
		return res, errors.Join(errs...)
	}
	alerts, err := anomaly.Alert(ctx, pool, j.deps.Send, env.Tenant.ID)
	if err != nil {
		errs = append(errs, fmt.Errorf("anomaly alert: %w", err))
	}
	res.Sent, res.Failed = alerts.Sent, alerts.Failed
	return res, errors.Join(errs...)
}

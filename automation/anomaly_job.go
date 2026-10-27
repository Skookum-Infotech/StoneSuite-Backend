package automation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/anomaly"
	"stonesuite-backend/companyprofile"
	"stonesuite-backend/tenancy"
)

// anomalyScanJobName identifies the anomaly scan job in logs.
const anomalyScanJobName = "anomaly_scan"

// anomalyScanLockKey is the Postgres advisory lock that keeps two anomaly scans
// of one tenant database from running at once (the 15-minute tick and a manual
// "scan now" share it). Distinct from the tick lock.
const anomalyScanLockKey int64 = 0x5354_414e_4f4d_3031 // "STANOM01"

var (
	// ErrAnomalyScanOff is returned when the workspace has switched the scan off.
	ErrAnomalyScanOff = errors.New("anomaly scanning is switched off for this workspace")
	// ErrAnomalyScanBusy is returned when another scan of this workspace is running.
	ErrAnomalyScanBusy = errors.New("an anomaly scan is already running for this workspace")
)

// AnomalyScan runs the anomaly rules against the tenant database and announces
// each new finding to the people who can act on it (see package anomaly).
type AnomalyScan struct {
	deps Deps
}

// NewAnomalyScan builds the job for callers outside the tick (the manual scan).
func NewAnomalyScan(d Deps) *AnomalyScan { return &AnomalyScan{deps: d} }

// Name identifies the job in logs.
func (*AnomalyScan) Name() string { return anomalyScanJobName }

// Run is the tick entry point. A switched-off workspace and a scan already in
// progress are both quiet no-ops here.
func (j *AnomalyScan) Run(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	res, err := j.scan(ctx, pool, env)
	if errors.Is(err, ErrAnomalyScanOff) || errors.Is(err, ErrAnomalyScanBusy) {
		slog.InfoContext(ctx, "anomaly scan skipped", "tenant_id", env.Tenant.ID, "reason", err.Error())
		return Result{}, nil
	}
	return res, err
}

// ScanNow runs one scan and alert pass for a tenant on demand, using the tenant's
// own calendar date. It returns ErrAnomalyScanOff or ErrAnomalyScanBusy rather
// than doing nothing silently, so the caller can tell the person who asked.
func (j *AnomalyScan) ScanNow(ctx context.Context, t *tenancy.Tenant, pool *pgxpool.Pool, now time.Time, fallback *time.Location) (Result, error) {
	profile, err := companyprofile.Get(ctx, pool)
	if err != nil {
		return Result{}, fmt.Errorf("load company profile: %w", err)
	}
	return j.scan(ctx, pool, newJobEnv(t, now, profile.Timezone, fallback))
}

// scan does nothing while the workspace has the anomaly_scan rule off. Otherwise
// it scans as the system actor, then announces pending findings. A scan error
// does not skip the alert pass, so findings stored by an earlier run still go
// out; both errors are returned together.
func (j *AnomalyScan) scan(ctx context.Context, pool *pgxpool.Pool, env JobEnv) (Result, error) {
	ctx = WithSystemActor(ctx)
	if _, on, err := loadRule(ctx, pool, RuleKeyAnomalyScan); err != nil {
		return Result{}, fmt.Errorf("anomaly rule: %w", err)
	} else if !on {
		return Result{}, ErrAnomalyScanOff
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("anomaly scan lock connection: %w", err)
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, anomalyScanLockKey).Scan(&locked); err != nil {
		return Result{}, fmt.Errorf("anomaly scan lock: %w", err)
	}
	if !locked {
		return Result{}, ErrAnomalyScanBusy
	}
	// Background ctx: the unlock must run even if ctx was cancelled, or the
	// session-level lock would ride back into the pool still held.
	defer func() {
		if _, uerr := conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, anomalyScanLockKey); uerr != nil {
			slog.ErrorContext(ctx, "anomaly scan unlock failed", "error", uerr)
		}
	}()

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

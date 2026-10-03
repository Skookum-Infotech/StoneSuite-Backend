package docextractjob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docextract"
	"stonesuite-backend/metrics"
	"stonesuite-backend/storage"
)

// Job outcomes (metrics label values).
const (
	outcomeSuccess = "success"
	outcomeDead    = "dead"
	outcomeRetry   = "retry"
	outcomeSkipped = "skipped"
)

// Pipeline stage names (metrics label values).
const (
	stageDownload = "download"
	stageExtract  = "extract"
	stageResolve  = "resolve"
	stagePersist  = "persist"
)

// objectStore is the slice of the R2 client the pipeline and sweeper use.
type objectStore interface {
	GetLimited(ctx context.Context, key string, maxBytes int64) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

// outcome is how one job attempt ended.
type outcome struct {
	kind   string // outcomeSuccess | outcomeDead | outcomeRetry | outcomeSkipped
	code   string // failure code for dead outcomes
	method string // metrics method label
	err    error
}

// dead builds a terminal-failure outcome.
func dead(code string, err error) outcome {
	return outcome{kind: outcomeDead, code: code, err: err, method: methodNone}
}

// retry builds a transient-failure outcome.
func retry(err error) outcome {
	return outcome{kind: outcomeRetry, code: FailTransient, err: err, method: methodNone}
}

// jobRunner runs the extraction pipeline for one tenant.
type jobRunner struct {
	store    *Store
	pool     *pgxpool.Pool
	objects  objectStore // nil when the tenant has no bucket
	cfg      Config
	llm      ragcore.LLMClient // nil disables the fallback
	waker    Waker
	notify   NotifyFunc
	tenantID string
}

// classifyExtractError maps a docextract failure to an outcome: an input
// error can never succeed on retry, anything else (cancellation, ...) can.
func classifyExtractError(err error) outcome {
	var in *docextract.InputError
	if errors.As(err, &in) {
		return dead(string(in.Code), err)
	}
	return retry(err)
}

// classifyDownloadError maps an object-store failure to an outcome.
func classifyDownloadError(err error) outcome {
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return dead(FailUploadMissing, err)
	case errors.Is(err, storage.ErrTooLarge):
		return dead(string(docextract.FailTooLarge), err)
	default:
		return retry(err)
	}
}

// process runs one extraction end to end. A panic anywhere is recovered as a
// terminal corrupt failure.
func (j *jobRunner) process(ctx context.Context, extractionID string) (out outcome) {
	defer func() {
		if x := recover(); x != nil {
			out = dead(string(docextract.FailCorrupt), fmt.Errorf("pipeline panic: %v", x))
		}
	}()

	ex, err := j.store.GetInternal(ctx, extractionID)
	if errors.Is(err, ErrNotFound) {
		return outcome{kind: outcomeSkipped, method: methodNone}
	}
	if err != nil {
		return retry(err)
	}
	if (ex.Status != StatusQueued && ex.Status != StatusRunning) || !ex.ExpiresAt.After(time.Now()) {
		return outcome{kind: outcomeSkipped, method: methodNone}
	}
	if err := j.store.MarkRunning(ctx, ex.ID); err != nil {
		if errors.Is(err, ErrConflict) {
			return outcome{kind: outcomeSkipped, method: methodNone}
		}
		return retry(err)
	}

	t0 := time.Now()
	data, derr := j.download(ctx, ex)
	metrics.ObserveDocExtractStage(stageDownload, time.Since(t0).Seconds())
	if derr != nil {
		return classifyDownloadError(derr)
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if ex.ClientSHA256 != "" && !strings.EqualFold(ex.ClientSHA256, sha) {
		return dead(FailUploadCorrupted, fmt.Errorf("upload checksum mismatch"))
	}

	own, err := loadOwnCompanyNames(ctx, j.pool)
	if err != nil {
		return retry(err)
	}

	t0 = time.Now()
	extracted, method, usedLLM, metricMethod, eout := j.extractOrCache(ctx, ex, data, sha, own)
	metrics.ObserveDocExtractStage(stageExtract, time.Since(t0).Seconds())
	if eout != nil {
		return *eout
	}

	t0 = time.Now()
	doc, rerr := ResolveDoc(ctx, j.pool, ex, sha, extracted, own)
	metrics.ObserveDocExtractStage(stageResolve, time.Since(t0).Seconds())
	if rerr != nil {
		return retry(rerr)
	}

	t0 = time.Now()
	model := ""
	if usedLLM {
		model = j.cfg.Model
	}
	serr := j.store.SaveResult(ctx, ex.ID, SaveResultParams{SHA256: sha, Method: method, Model: model, Result: doc})
	metrics.ObserveDocExtractStage(stagePersist, time.Since(t0).Seconds())
	if errors.Is(serr, ErrConflict) {
		return outcome{kind: outcomeSkipped, method: methodNone}
	}
	if serr != nil {
		return retry(serr)
	}
	j.maybeNotify(ctx, ex.ID)
	return outcome{kind: outcomeSuccess, method: metricMethod}
}

// download fetches the staged object, bounded by the size cap.
func (j *jobRunner) download(ctx context.Context, ex *Extraction) ([]byte, error) {
	if j.objects == nil {
		return nil, storage.ErrStorageNotConfigured
	}
	data, err := j.objects.GetLimited(ctx, ex.StagingKey, j.cfg.Limits.MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("fetch staged document: %w", err)
	}
	return data, nil
}

// extractOrCache copies the result of an earlier extraction of the same file
// when one exists (zero parse and LLM cost), else runs docextract.Extract. The
// returned method is what is stored; metricMethod is the metrics label.
func (j *jobRunner) extractOrCache(ctx context.Context, ex *Extraction, data []byte, sha string, own []string) (res docextract.Result, method string, usedLLM bool, metricMethod string, fail *outcome) {
	hit, err := j.store.FindCacheHit(ctx, ex.DocType, sha, j.cfg.StagingTTL, ex.ID)
	if err != nil {
		o := retry(err)
		return res, "", false, "", &o
	}
	if hit != nil {
		var cached ResultDoc
		if err := json.Unmarshal(hit.Result, &cached); err == nil {
			return cached.Extracted, hit.Method, hit.Model != "", MethodCache, nil
		}
	}

	opts := docextract.Options{
		Limits:          j.cfg.Limits,
		OwnCompanyNames: own,
		TokenBudget:     j.cfg.TokenBudget,
		LLMTimeout:      j.cfg.LLMTimeout,
	}
	adapter := &llmAdapter{client: j.llm, waker: j.waker}
	if j.llm != nil {
		opts.LLM = adapter
	}
	res, method, err = docextract.Extract(ctx, data, docextract.DocType(ex.DocType), opts)
	if adapter.calls > 0 {
		metrics.ObserveDocExtractTokens(adapter.promptTokens, adapter.completionTokens)
	}
	if err != nil {
		o := classifyExtractError(err)
		return res, "", false, "", &o
	}
	return res, method, adapter.calls > 0, method, nil
}

// maybeNotify re-reads the row (the user may have closed the dialog and set
// notify_on_complete while the job ran) and notifies the uploader if asked.
func (j *jobRunner) maybeNotify(ctx context.Context, id string) {
	ex, err := j.store.GetInternal(ctx, id)
	if err != nil || !ex.NotifyOnComplete || ex.Status != StatusReady {
		return
	}
	notifyReady(ctx, j.notify, j.tenantID, ex)
}

// loadOwnCompanyNames reads the tenant's own names from company_profile; a
// customer PO names the tenant as the vendor, so they are excluded from
// customer detection and matching.
func loadOwnCompanyNames(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	var name, legal string
	rows, err := pool.Query(ctx, `SELECT company_name, legal_name FROM company_profile LIMIT 1`)
	if err != nil {
		return nil, fmt.Errorf("load company names: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&name, &legal); err != nil {
			return nil, fmt.Errorf("scan company names: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate company names: %w", err)
	}
	var out []string
	for _, n := range []string{name, legal} {
		if strings.TrimSpace(n) != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

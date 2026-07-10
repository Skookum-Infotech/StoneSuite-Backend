package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	sentry "github.com/getsentry/sentry-go"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/ai"
	"stonesuite-backend/ai/index"
	"stonesuite-backend/config"
	"stonesuite-backend/controllers"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/database"
	"stonesuite-backend/jobqueue"
	"stonesuite-backend/logship"
	"stonesuite-backend/metrics"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/provisioning"
	"stonesuite-backend/secret"
	"stonesuite-backend/services"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
)

func main() {
	// 1. Load Configurations and fail fast on insecure/invalid config.
	config.Load()
	if err := config.AppConfig.Validate(); err != nil {
		log.Fatalf("CRITICAL ERROR: %v", err)
	}

	// Graceful shutdown context: SIGTERM or Ctrl-C cancels this context. All
	// background goroutines (rate-limiter eviction, log shipper, etc.) derive
	// their lifetime from it. Created first so they can all share it.
	shutdownCtx, stopSignal := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	// Structured JSON logging: one machine-parseable line per event. slog.Default()
	// is used by the request logger, panic-recovery, and security-event middleware.
	logLevel := slog.LevelInfo
	if !config.AppConfig.IsProduction() {
		logLevel = slog.LevelDebug
	}
	// Optional log shipping (U4): when AXIOM_TOKEN+AXIOM_DATASET are set, logs go
	// to Axiom in addition to stdout — no shipper VM, so scale-to-zero is intact.
	var logDst io.Writer = os.Stdout
	shipper := logship.New(config.AppConfig.AxiomToken, config.AppConfig.AxiomDataset)
	if shipper != nil {
		shipper.Start(shutdownCtx)
		logDst = io.MultiWriter(os.Stdout, shipper)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(logDst, &slog.HandlerOptions{Level: logLevel})))
	if shipper != nil {
		slog.Info("log shipping enabled", slog.String("sink", "axiom"))
	}

	// Error tracking (optional): when SENTRY_DSN is set, panics recovered by
	// middleware.Recover are reported to Sentry. No-op when unset.
	if dsn := config.AppConfig.SentryDSN; dsn != "" {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn:         dsn,
			Environment: config.AppConfig.Environment,
		}); err != nil {
			slog.Warn("sentry init failed", slog.String("error", err.Error()))
		} else {
			slog.Info("sentry error tracking enabled")
			// Flush buffered events on shutdown.
			defer sentry.Flush(2 * time.Second)
		}
	}

	// readyCheck backs the /readyz probe. Set once the control plane is up so
	// readiness reflects database reachability; nil means "process up" only.
	var readyCheck func(context.Context) error

	// Auth rate limiter: per-IP brute-force guard for unauthenticated endpoints
	// (login, password reset, activation). Token bucket: 10 immediate attempts,
	// then refills at 1 every 5s (~12/min sustained) per client IP.
	authRateLimiter := middleware.NewRateLimiter(shutdownCtx, 0.2, 10)

	// 2. Initialize the multi-tenant control plane (required).
	// When CONTROL_PLANE_DB_URL is set, requests can be resolved to a tenant and
	// routed to that tenant's isolated database. Until then, legacy routes work
	// unchanged so existing functionality is not disrupted during the migration.
	var resolver *tenancy.Resolver
	var tenantOps *controllers.TenantOps
	var userOps *controllers.UserOps
	var crmAdminOps *controllers.CRMAdminOps
	var provisioner *provisioning.Provisioner
	var cpPool *pgxpool.Pool     // control-plane pool; used by AIOps for cp_rag_chunks
	var cp *tenancy.ControlPlane // control-plane handle; also used by AIOps for the reindex-help platform-admin check
	var ollamaLifecycle *services.OllamaLifecycle
	if config.AppConfig.ControlPlaneDBURL != "" {
		var err error
		cp, err = tenancy.NewControlPlane(context.Background(), config.AppConfig.ControlPlaneDBURL)
		if err != nil {
			log.Fatalf("CRITICAL ERROR: Failed to initialize control plane: %v", err)
		}
		cpPool = cp.Pool()

		// Readiness now reflects control-plane DB reachability.
		readyCheck = func(ctx context.Context) error { return cp.Pool().Ping(ctx) }

		// Auto-apply control-plane migrations on every startup (idempotent).
		// This creates tenants/identities/invites/etc. tables on a fresh Neon DB
		// and applies any new migrations on subsequent deploys.
		if err := database.ApplyControlPlaneMigrations(context.Background(), cp.Pool()); err != nil {
			log.Fatalf("CRITICAL ERROR: control-plane migrations failed: %v", err)
		}
		log.Println("Control-plane migrations: ok")

		// First-boot: if PLATFORM_ADMIN_EMAIL is set and no owner exists, create
		// the owner tenant + identity and print a one-time setup token to stdout.
		seedPlatformOwner(context.Background(), cp)

		// Secret cipher (optional): when configured, tenant DSNs are encrypted
		// at rest and decrypted on use; otherwise DSNs are stored in plaintext (dev).
		var cipher *secret.Cipher
		var dsnResolver tenancy.DSNResolver
		if config.AppConfig.SecretEncryptionKey != "" {
			cipher, err = secret.New(config.AppConfig.SecretEncryptionKey)
			if err != nil {
				log.Fatalf("CRITICAL ERROR: invalid SECRET_ENCRYPTION_KEY: %v", err)
			}
			dsnResolver = func(_ context.Context, t *tenancy.Tenant) (string, error) {
				return cipher.Decrypt(t.DBConnectionRef)
			}
			log.Println("Tenant DSN encryption: enabled.")
		} else {
			log.Println("Tenant DSN encryption: disabled (plaintext DSNs — development only).")
		}
		tenantRouter := tenancy.NewRouter(dsnResolver) // nil resolver -> PlainDSNResolver
		resolver = tenancy.NewResolver(cp, tenantRouter)

		// Durable job queue (async_jobs table): backs tenant provisioning and
		// future long-running work (e.g. workflow transition actions).
		jobQueue := jobqueue.New(cp.Pool())

		// Cloudflare client — shared by provisioner and admin repair endpoints.
		cfClient := storage.NewCFClient(config.AppConfig.CloudflareAccountID, config.AppConfig.CloudflareAPIToken)
		if cfClient.IsConfigured() {
			log.Println("R2 per-tenant buckets: Cloudflare API configured.")
		}
		corsOrigins := []string{}
		for _, o := range strings.Split(config.AppConfig.CorsOrigin, ",") {
			if o = strings.TrimSpace(o); o != "" {
				corsOrigins = append(corsOrigins, o)
			}
		}

		// Provisioner (optional): requires an admin DSN to create tenant databases.
		if config.AppConfig.ProvisionAdminDBURL != "" {
			provider, perr := provisioning.NewSQLProvider(config.AppConfig.ProvisionAdminDBURL)
			if perr != nil {
				log.Fatalf("CRITICAL ERROR: invalid PROVISION_ADMIN_DB_URL: %v", perr)
			}
			provisioner = provisioning.New(cp, provider, cipher, jobQueue)
			provisioner.WithCFClient(cfClient, corsOrigins)
			provisioner.Start(2)
			log.Println("Tenant provisioner started (2 workers, durable queue).")

			// Self-heal: the platform owner is a first-class tenant too. If its
			// workspace database was never provisioned (e.g. the owner was seeded
			// by hand), provision it now so platform admins get a working
			// workspace (workflows/roles) like any tenant.
			ensureOwnerWorkspace(context.Background(), cp, provisioner)

			// Fan-out migrations: apply schema.sql to all already-provisioned
			// tenant DBs. Idempotent (CREATE IF NOT EXISTS / ON CONFLICT DO
			// NOTHING) so it's safe — and necessary — to re-run on every boot;
			// run synchronously so RAG workers below never start against a
			// tenant DB that hasn't picked up new tables yet (e.g. rag_index_queue).
			migrateAllTenants(context.Background(), cp, tenantRouter)

			// Ollama embedder box lifecycle: started here (in parallel with the
			// rest of boot, since a cold model load takes ~10s) so it's tied to
			// this backend process's own lifetime rather than left to Fly
			// Proxy's flycast autostart, which was verified unreliable for this
			// deployment (see docs/ai-assistant.md). Stopped on graceful
			// shutdown below. Skipped entirely if unconfigured (e.g. local dev).
			if config.AppConfig.FlyOllamaAPIToken != "" {
				ollamaLifecycle = services.NewOllamaLifecycle(config.AppConfig.FlyOllamaAppName, config.AppConfig.FlyOllamaAPIToken)
				go func() {
					if err := ollamaLifecycle.StartAll(context.Background()); err != nil {
						log.Printf("ollama-lifecycle: start failed: %v", err)
						return
					}
					// Fire a throwaway embed+chat so the first REAL /ai/ask
					// request isn't the one paying Ollama's model-load
					// latency — StartAll only boots the Machine, models load
					// lazily on first inference. Best-effort: a failed
					// warmup just means the first real request pays that
					// latency itself, same as before this existed.
					warmupEmb := ai.NewOllamaQueryEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel)
					warmupLLM := ai.NewOllamaLLMClient(config.AppConfig.OllamaBaseURL, config.AppConfig.AIChatModel)
					if err := ai.WarmUp(context.Background(), warmupEmb, warmupLLM); err != nil {
						log.Printf("ollama-lifecycle: warmup failed: %v", err)
					}
				}()
			}

			// RAG index workers: one per active tenant, draining rag_index_queue
			// on a ticker so record writes become fresh vectors within seconds
			// (see ai/index.Worker). Tied to shutdownCtx since these are
			// long-running loops that must stop on server shutdown.
			go startRAGIndexing(shutdownCtx, cp, tenantRouter)
		} else {
			log.Println("Note: PROVISION_ADMIN_DB_URL not set — tenant provisioning disabled.")
		}

		tenantOps = controllers.NewTenantOps(cp, provisioner, tenantRouter, jobQueue).
			WithCFClient(cfClient, corsOrigins)
		userOps = controllers.NewUserOps(cp, tenantRouter)
		crmAdminOps = controllers.NewCRMAdminOps(cp)
		log.Println("Multi-tenant control plane initialized.")
	} else {
		log.Fatalf("CRITICAL ERROR: CONTROL_PLANE_DB_URL is required (the legacy single-tenant backend has been removed).")
	}

	// 3. Setup HTTP Routing
	mux := http.NewServeMux()

	// Root API Info Route
	mux.HandleFunc("/api", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "Method not allowed"})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(struct {
			Success bool   `json:"success"`
			Message string `json:"message"`
			Version string `json:"version"`
		}{
			Success: true,
			Message: "Welcome to the StoneSuite Go Authentication Backend API.",
			Version: "1.0.0",
		})
	})

	// Observability probes (U1/U2). Liveness never touches the DB; readiness
	// pings the control-plane pool; metrics is the Prometheus scrape target.
	health := controllers.NewHealthOps(readyCheck)
	mux.HandleFunc("GET /api/healthz", health.Healthz)
	mux.HandleFunc("GET /api/readyz", health.Readyz)

	// /api/metrics — Prometheus exposition. Optionally bearer-token protected
	// (METRICS_TOKEN); Fly's built-in Prometheus scrapes this for free.
	var metricsHandler http.Handler = metrics.Handler()
	if tok := config.AppConfig.MetricsToken; tok != "" {
		inner := metricsHandler
		metricsHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+tok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			inner.ServeHTTP(w, r)
		})
	}
	mux.Handle("GET /api/metrics", metricsHandler)

	// Multi-tenant routes (the control plane is always configured at this point).
	if tenantOps != nil {
		// Public: tenant-scoped login + password recovery. All credential-checking
		// endpoints sit behind the per-IP rate limiter to blunt brute-force and
		// credential-stuffing attacks (no tenant id exists yet to key on).
		mux.Handle("/api/auth/tenant-login", authRateLimiter.PerIPFunc(tenantOps.TenantLogin))
		mux.Handle("POST /api/auth/refresh", authRateLimiter.PerIPFunc(tenantOps.RefreshSession))
		mux.HandleFunc("POST /api/auth/logout", tenantOps.Logout)
		mux.Handle("POST /api/auth/change-password", middleware.RequireAuth(http.HandlerFunc(tenantOps.ChangePassword)))
		mux.Handle("POST /api/auth/forgot-password", authRateLimiter.PerIPFunc(tenantOps.ForgotPassword))
		mux.HandleFunc("GET /api/auth/reset-password/{token}", tenantOps.ValidateResetToken)
		mux.Handle("POST /api/auth/reset-password", authRateLimiter.PerIPFunc(tenantOps.ResetPassword))

		// Public: self-service onboarding (fill form → approval → set password).
		mux.HandleFunc("/api/onboarding/form-schema", tenantOps.FormSchema)
		mux.HandleFunc("/api/onboarding/apply/", tenantOps.GetApply) // GET /{token}
		mux.HandleFunc("/api/onboarding/apply", tenantOps.SubmitApply)
		mux.HandleFunc("/api/onboarding/set-password/", tenantOps.GetSetPassword) // GET /{token}
		mux.HandleFunc("/api/onboarding/set-password", tenantOps.SetPassword)

		// Platform setup: status probe + one-shot token activation (no auth).
		mux.HandleFunc("GET /api/platform/setup/status", tenantOps.SetupStatus)
		mux.Handle("POST /api/platform/activate", authRateLimiter.PerIPFunc(tenantOps.Activate))

		// Platform-admin: tenant management (auth required; admin checked inside).
		mux.Handle("POST /api/platform/tenants/{id}/repair-cors", middleware.RequireAuth(http.HandlerFunc(tenantOps.RepairBucketCORS)))
		mux.Handle("/api/platform/invites", middleware.RequireAuth(http.HandlerFunc(tenantOps.InviteCustomer)))
		mux.Handle("/api/platform/tenants", middleware.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet:
				tenantOps.ListTenants(w, r)
			case http.MethodPost:
				tenantOps.CreateTenant(w, r)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		})))
		mux.Handle("/api/platform/tenants/", middleware.RequireAuth(http.HandlerFunc(tenantOps.TenantLifecycle)))
	}

	// Tenant-scoped demo route: JWT -> tenant resolution -> per-tenant DB query.
	if resolver != nil {
		tenantMe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenant, err := tenancy.TenantFromContext(r.Context())
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "tenant not resolved"})
				return
			}
			pool, err := tenancy.PoolFromContext(r.Context())
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "tenant pool not resolved"})
				return
			}
			// Prove we are querying THIS tenant's isolated database.
			var userCount int
			if err := pool.QueryRow(r.Context(), "SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: "tenant db query failed: " + err.Error()})
				return
			}
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(struct {
				Success      bool   `json:"success"`
				TenantID     string `json:"tenantId"`
				TenantSlug   string `json:"tenantSlug"`
				TenantName   string `json:"tenantName"`
				TenantDBName string `json:"tenantDbName"`
				UserCount    int    `json:"userCount"`
			}{true, tenant.ID, tenant.Slug, tenant.DisplayName, tenant.DBName, userCount})
		})
		// Per-tenant rate limit: 20 req/sec sustained, bursts up to 40, before
		// any tenant DB work happens (ADR-3). Platform-admin/legacy requests
		// (no tenant_id) pass through unlimited.
		// shutdownCtx is defined below; the rate limiter's eviction goroutine
		// exits when the server shuts down.
		tenantRateLimiter := middleware.NewRateLimiter(shutdownCtx, 20, 40)

		// AI-specific rate limit: 0.5 req/sec sustained (30/min), bursts up to 5,
		// per tenant. The generic tenantRateLimiter above is calibrated for cheap
		// CRUD calls; every /ai/ask request costs a real embedding + LLM call, so
		// it needs its own, much tighter budget to bound a single tenant's AI
		// spend rather than relying on the CRUD-sized limit above.
		aiRateLimiter := middleware.NewRateLimiter(shutdownCtx, 0.5, 5)

		mux.Handle("/api/tenant/me", middleware.RequireAuth(tenantRateLimiter.PerTenant(resolver.Middleware(tenantMe))))

		// tenantChain applies RequireAuth → per-tenant rate limit → tenancy
		// resolver before every handler.
		tenantChain := func(h http.HandlerFunc) http.Handler {
			return middleware.RequireAuth(tenantRateLimiter.PerTenant(resolver.Middleware(h)))
		}

		// aiChain layers the AI-specific rate limit on top of tenantChain's
		// generic one, for routes that make a synchronous embedding/LLM call.
		aiChain := func(h http.HandlerFunc) http.Handler {
			return middleware.RequireAuth(aiRateLimiter.PerTenant(tenantRateLimiter.PerTenant(resolver.Middleware(h))))
		}

		// Tenant-scoped RBAC management (role editor API). Each handler runs
		// after RequireAuth + the tenancy resolver, then enforces the relevant
		// catalog permission (role:read / role:create / role:update / role:delete) per method.
		rbac := controllers.NewRBACOps()
		mux.Handle("/api/tenant/permissions/catalog", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.Catalog))))
		mux.Handle("/api/tenant/roles", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.Roles))))
		mux.Handle("/api/tenant/roles/", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.Role))))
		// Active-role context switching: sets/clears which one of the caller's
		// assigned roles is currently enforced (see authz.EffectiveGrants).
		mux.Handle("POST /api/tenant/auth/switch-role", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.SwitchRole))))

		// Tenant-scoped user management. Method+path patterns are more specific
		// than the catch-all /api/tenant/users/ below and take precedence.
		mux.Handle("GET /api/tenant/users/me/permissions", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.MyPermissions))))
		mux.Handle("GET /api/tenant/users", tenantChain(userOps.ListUsers))
		mux.Handle("POST /api/tenant/users/invite", tenantChain(userOps.InviteUser))
		mux.Handle("GET /api/tenant/users/{id}", tenantChain(userOps.GetUser))
		mux.Handle("PATCH /api/tenant/users/{id}", tenantChain(userOps.UpdateUser))
		mux.Handle("DELETE /api/tenant/users/{id}", tenantChain(userOps.DeactivateUser))

		// Role assignment/revocation (existing, kept as catch-all for /users/{id}/roles paths).
		mux.Handle("/api/tenant/users/", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.UserRoles))))

		// Workspace invite management.
		mux.Handle("GET /api/tenant/invites", tenantChain(userOps.ListInvites))
		mux.Handle("POST /api/tenant/invites/{id}/resend", tenantChain(userOps.ResendInvite))
		mux.Handle("DELETE /api/tenant/invites/{id}", tenantChain(userOps.RevokeInvite))

		// Public: accept workspace user invite (no auth required).
		// /accept is registered first so it is not consumed by the token catch-all.
		mux.HandleFunc("POST /api/onboarding/user-invite/accept", userOps.AcceptUserInvite)
		mux.HandleFunc("GET /api/onboarding/user-invite/{token}", userOps.GetUserInvite)

		// Tenant-scoped workflow engine + records (Phase 3).
		wf := controllers.NewWorkflowOps()
		mux.Handle("GET /api/tenant/workflows", tenantChain(wf.ListWorkflows))
		mux.Handle("GET /api/tenant/workflows/{id}", tenantChain(wf.GetWorkflow))
		mux.Handle("POST /api/tenant/workflows/{id}/enabled", tenantChain(wf.SetWorkflowEnabled))
		mux.Handle("POST /api/tenant/workflows/{id}/fields", tenantChain(wf.CreateField))
		mux.Handle("DELETE /api/tenant/workflows/{id}/fields/{fieldId}", tenantChain(wf.DeleteField))
		mux.Handle("GET /api/tenant/workflows/{id}/numbering", tenantChain(wf.GetNumberingConfig))
		mux.Handle("GET /api/tenant/workflows/{id}/approvers", tenantChain(wf.GetWorkflowApprovers))
		mux.Handle("PATCH /api/tenant/workflows/{id}/approvers", tenantChain(wf.SetWorkflowApprovers))
		mux.Handle("GET /api/tenant/workflows/{id}/states/{stateId}/approvers", tenantChain(wf.GetStateApprovers))
		mux.Handle("PUT /api/tenant/workflows/{id}/states/{stateId}/approvers", tenantChain(wf.SetStateApprovers))
		mux.Handle("PUT /api/tenant/workflows/{id}/numbering", tenantChain(wf.SetNumberingConfig))
		mux.Handle("GET /api/tenant/workflows/{id}/records", tenantChain(wf.ListRecords))
		mux.Handle("POST /api/tenant/workflows/{id}/records/search", tenantChain(wf.SearchRecords))
		mux.Handle("POST /api/tenant/workflows/{id}/records", tenantChain(wf.CreateRecord))
		mux.Handle("GET /api/tenant/records/approvals/pending", tenantChain(wf.PendingApprovalsQueue))
		mux.Handle("GET /api/tenant/records/{id}", tenantChain(wf.GetRecord))
		mux.Handle("PATCH /api/tenant/records/{id}", tenantChain(wf.UpdateRecord))
		mux.Handle("POST /api/tenant/records/{id}/transition", tenantChain(wf.TransitionRecord))
		mux.Handle("POST /api/tenant/records/{id}/approve", tenantChain(wf.ApproveRecord))

		// Record attachments (Cloudflare R2). r2Client is nil when R2 env vars are
		// absent — presign/download endpoints return 503; list/metadata still work.
		r2Client, r2Err := storage.New(config.AppConfig)
		if r2Err != nil {
			log.Printf("WARNING: R2 storage client failed to initialise: %v", r2Err)
		}
		if r2Client != nil {
			log.Println("R2 storage: configured.")
		} else {
			log.Println("R2 storage: not configured (attachment upload/download endpoints will return 503).")
		}
		attachOps := controllers.NewAttachmentOps(r2Client)
		// presign-batch must be registered before the bare /attachments routes so
		// the more-specific pattern wins in Go's http.ServeMux.
		mux.Handle("POST /api/tenant/records/{id}/attachments/presign-batch", tenantChain(attachOps.PresignBatch))
		mux.Handle("POST /api/tenant/records/{id}/attachments", tenantChain(attachOps.ConfirmAttachments))
		mux.Handle("GET /api/tenant/records/{id}/attachments", tenantChain(attachOps.ListAttachments))
		mux.Handle("GET /api/tenant/records/{id}/attachments/{attachmentId}/download", tenantChain(attachOps.DownloadAttachment))
		mux.Handle("DELETE /api/tenant/records/{id}/attachments/{attachmentId}", tenantChain(attachOps.DeleteAttachment))

		// Unified CRM: lead, prospect, customer all backed by workflow_records.
		crm := controllers.NewCRMOps()
		// Reference data for the unified CRM core-field selects (design-agnostic).
		crmLookups := controllers.NewCRMLookups()
		mux.Handle("GET /api/tenant/crm/lookups", tenantChain(crmLookups.GetLookups))
		// Status / dropdown endpoints.
		mux.Handle("GET /api/tenant/crm/statuses", tenantChain(crm.AllStatuses))
		mux.Handle("GET /api/tenant/crm/{workflowKey}/statuses", tenantChain(crm.WorkflowStatuses))
		// Per-workflow list + create.
		mux.Handle("GET /api/tenant/crm/{workflowKey}/records", tenantChain(crm.ListRecords))
		mux.Handle("POST /api/tenant/crm/{workflowKey}/records/search", tenantChain(crm.SearchRecords))
		mux.Handle("POST /api/tenant/crm/{workflowKey}/records", tenantChain(crm.CreateRecord))
		// Single-record CRUD and state machine. workflowKey is accepted but ignored
		// by the handlers — they load the record by id and derive the workflow.
		// Using /{workflowKey}/records/{id} avoids ambiguity with /{workflowKey}/statuses.
		mux.Handle("GET /api/tenant/crm/{workflowKey}/records/{id}", tenantChain(crm.GetRecord))
		mux.Handle("PATCH /api/tenant/crm/{workflowKey}/records/{id}", tenantChain(crm.UpdateRecord))
		mux.Handle("DELETE /api/tenant/crm/{workflowKey}/records/{id}", tenantChain(crm.DeleteRecord))
		mux.Handle("GET /api/tenant/crm/{workflowKey}/records/{id}/transitions", tenantChain(crm.AvailableTransitions))
		mux.Handle("POST /api/tenant/crm/{workflowKey}/records/{id}/transition", tenantChain(crm.TransitionRecord))
		mux.Handle("POST /api/tenant/crm/{workflowKey}/records/{id}/convert", tenantChain(crm.ConvertRecord))
		// Approval: sign off a Closed-Won customer (v2 design).
		mux.Handle("POST /api/tenant/crm/{workflowKey}/records/{id}/approve", tenantChain(crm.ApproveRecord))
		mux.Handle("GET /api/tenant/crm/{workflowKey}/approvals/pending", tenantChain(crm.PendingApprovals))
		// Per-record audit trail.
		mux.Handle("GET /api/tenant/crm/{workflowKey}/records/{id}/audit", tenantChain(crm.RecordAudit))

		// CRM admin: switch the tenant's database design, and configure approvers.
		mux.Handle("GET /api/tenant/admin/design-version", tenantChain(crmAdminOps.GetDesignVersion))
		mux.Handle("POST /api/tenant/admin/design-version", tenantChain(crmAdminOps.SetDesignVersion))
		mux.Handle("GET /api/tenant/config/approvers", tenantChain(crmAdminOps.ListApprovers))
		mux.Handle("POST /api/tenant/config/approvers", tenantChain(crmAdminOps.CreateApprover))
		mux.Handle("DELETE /api/tenant/config/approvers/{id}", tenantChain(crmAdminOps.DeleteApprover))

		// Inventory: shared item catalog (Sales Order line items reference it).
		inv := controllers.NewInventoryOps()
		mux.Handle("GET /api/tenant/inventory/items", tenantChain(inv.List))
		mux.Handle("POST /api/tenant/inventory/items/search", tenantChain(inv.Search))
		mux.Handle("POST /api/tenant/inventory/items", tenantChain(inv.Create))
		mux.Handle("GET /api/tenant/inventory/items/{uuid}", tenantChain(inv.Get))
		mux.Handle("PATCH /api/tenant/inventory/items/{uuid}", tenantChain(inv.Update))
		mux.Handle("DELETE /api/tenant/inventory/items/{uuid}", tenantChain(inv.Delete))

		// Sales Order: dedicated relational module (header + line items), a
		// sibling of the CRM customer table — not served through the generic
		// /api/tenant/crm/{workflowKey} JSONB router.
		so := controllers.NewSalesOrderOps()
		mux.Handle("GET /api/tenant/sales-orders", tenantChain(so.List))
		mux.Handle("POST /api/tenant/sales-orders/search", tenantChain(so.Search))
		mux.Handle("POST /api/tenant/sales-orders", tenantChain(so.Create))
		mux.Handle("GET /api/tenant/sales-orders/{uuid}", tenantChain(so.Get))
		mux.Handle("PATCH /api/tenant/sales-orders/{uuid}", tenantChain(so.Update))
		mux.Handle("DELETE /api/tenant/sales-orders/{uuid}", tenantChain(so.Delete))
		mux.Handle("POST /api/tenant/sales-orders/{uuid}/transition", tenantChain(so.Transition))
		mux.Handle("GET /api/tenant/sales-orders/{uuid}/inventory", tenantChain(so.Inventory))
		mux.Handle("GET /api/tenant/sales-orders/{uuid}/audit", tenantChain(so.Audit))

		// AI assistant: RBAC-scoped RAG chat over CRM records + app help.
		// Both embeddings and chat are self-hosted on the same Ollama box
		// (ADR-001) — no third-party LLM account, API key, or quota.
		aiOps := controllers.NewAIOps(
			cpPool,
			ai.NewOllamaQueryEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel),
			ai.NewOllamaLLMClient(config.AppConfig.OllamaBaseURL, config.AppConfig.AIChatModel),
			cp,
			ai.NewOllamaDocEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel),
		)
		mux.Handle("POST /api/tenant/ai/ask", aiChain(aiOps.Ask))
		mux.Handle("POST /api/tenant/ai/reindex", tenantChain(aiOps.Reindex))
		mux.Handle("POST /api/platform/ai/reindex-help", middleware.RequireAuth(http.HandlerFunc(aiOps.ReindexHelp)))
	}

	// Build the CORS allowlist once from the comma-separated CORS_ORIGIN value.
	allowedOrigins := make(map[string]bool)
	for _, o := range strings.Split(config.AppConfig.CorsOrigin, ",") {
		if trimmed := strings.TrimSpace(o); trimmed != "" {
			allowedOrigins[trimmed] = true
		}
	}

	// 4. Global Middleware: CORS Policy Wrapper (request logging + panic recovery
	// are applied as an outer chain below, so they observe every response).
	corsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Inject CORS Headers — echo the request origin only if it is in the allowlist.
		if origin := r.Header.Get("Origin"); allowedOrigins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		// Handle Preflight OPTIONS requests immediately
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Check for unknown routes under Mux
		// http.ServeMux in standard library redirects unmatched paths to most specific match.
		// Unmatched routes under ServeMux will fall through. Let's make sure we handle a standard 404 response
		// if the path doesn't start with registered prefixes.
		path := r.URL.Path
		if path != "/api" && path != "/api/healthz" && path != "/api/readyz" && path != "/api/metrics" && !strings.HasPrefix(path, "/api/auth/") && !strings.HasPrefix(path, "/api/onboarding") && !strings.HasPrefix(path, "/api/tenant") && !strings.HasPrefix(path, "/api/platform") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "API route not found.",
			})
			return
		}

		mux.ServeHTTP(w, r)
	})

	// Outer middleware chain (outermost first): RequestLogger assigns the
	// correlation id and logs every response; Recover turns any handler panic
	// into a clean 500 (logged with stack) instead of crashing the VM. Recover
	// is inside RequestLogger so panics are still logged as a 500 request line.
	globalHandler := middleware.RequestLogger(middleware.Recover(corsHandler))

	// 5. Start Server
	port := config.AppConfig.Port
	fmt.Println("===============================================")
	fmt.Println("  StoneSuite Go Login Backend is Running!      ")
	fmt.Printf("  Local Endpoint: http://localhost:%s\n", port)
	fmt.Printf("  Allowed CORS Origin: %s\n", config.AppConfig.CorsOrigin)
	fmt.Println("===============================================")

	server := &http.Server{
		Addr:    ":" + port,
		Handler: globalHandler,
		// WriteTimeout must exceed the slowest legitimate handler, not just the
		// common case: POST /api/tenant/ai/ask runs a synchronous self-hosted
		// LLM completion (ai.OllamaLLMClient, 60s inner client timeout) on top
		// of embedding + retrieval. A too-short WriteTimeout forcibly closes
		// the TCP connection once it elapses — even when the handler is about
		// to finish with a correct answer — which looks identical to a proxy
		// timeout from the client (silent "Network Error", no JSON body) but
		// is actually us, not Fly's edge, hanging up on our own slow success.
		// Set comfortably above OllamaLLMClient's timeout so that timeout
		// fires first and the client gets a clean error response instead.
		WriteTimeout: 90 * time.Second,
		ReadTimeout:  15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Run the server in a goroutine so we can block on the shutdown signal below.
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("CRITICAL SERVER FAILURE: %v", err)
		}
	}()

	// Block until SIGTERM or Ctrl-C is received.
	<-shutdownCtx.Done()
	stopSignal() // release signal resources

	log.Println("Shutting down: draining in-flight requests (up to 10s)...")
	if provisioner != nil {
		provisioner.Stop()
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutCtx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	}
	// Stop the Ollama embedder box alongside this process — see the matching
	// StartAll call above for why the backend, not Fly Proxy, owns this.
	if ollamaLifecycle != nil {
		if err := ollamaLifecycle.StopAll(shutCtx); err != nil {
			log.Printf("ollama-lifecycle: stop failed: %v", err)
		}
	}
	// Flush any buffered logs to Axiom before exit (worker already draining on
	// shutdownCtx cancellation; Stop waits for its final batch).
	if shipper != nil {
		shipper.Stop()
	}
	log.Println("Server stopped.")
}

// migrateAllTenants runs ApplyTenantMigrations on every provisioned tenant DB.
// Called synchronously on startup, before RAG workers start, so schema.sql
// (idempotent via CREATE IF NOT EXISTS / ON CONFLICT DO NOTHING) is applied
// to existing tenant DBs on every boot without manual intervention. There is
// no version gate: schema.sql is the single canonical source of truth and is
// always safe to re-run.
func migrateAllTenants(ctx context.Context, cp *tenancy.ControlPlane, router *tenancy.Router) {
	tenants, err := cp.ListTenants(ctx)
	if err != nil {
		log.Printf("migrate-all: failed to list tenants: %v", err)
		return
	}
	for _, t := range tenants {
		if !t.Servable() {
			continue
		}
		pool, err := router.PoolFor(ctx, &t)
		if err != nil {
			log.Printf("migrate-all: tenant %s: pool error: %v", t.Slug, err)
			continue
		}
		ver, err := database.ApplyTenantMigrations(ctx, pool)
		if err != nil {
			log.Printf("migrate-all: tenant %s: migration failed: %v", t.Slug, err)
			continue
		}
		if err := cp.SetTenantSchemaVersion(ctx, t.ID, ver); err != nil {
			log.Printf("migrate-all: tenant %s: update schema version failed: %v", t.Slug, err)
		} else {
			log.Printf("migrate-all: tenant %s migrated to v%d", t.Slug, ver)
		}
		if empty, verr := database.ValidateLookupSeeds(ctx, pool); verr != nil {
			log.Printf("migrate-all: tenant %s: lookup seed check failed: %v", t.Slug, verr)
		} else if len(empty) > 0 {
			log.Printf("migrate-all: tenant %s: WARNING unseeded CRM lookup tables: %v", t.Slug, empty)
		}
	}
}

// startRAGIndexing starts one index-drain loop and one reconciliation loop per
// active tenant. Runs once at boot (mirroring migrateAllTenants); tenants
// provisioned after this process started are picked up on the next restart —
// acceptable given the scale-to-zero deploy model restarts frequently.
func startRAGIndexing(ctx context.Context, cp *tenancy.ControlPlane, router *tenancy.Router) {
	tenants, err := cp.ListTenants(ctx)
	if err != nil {
		log.Printf("rag-index: failed to list tenants: %v", err)
		return
	}
	for _, t := range tenants {
		if !t.Servable() {
			continue
		}
		pool, err := router.PoolFor(ctx, &t)
		if err != nil {
			log.Printf("rag-index: tenant %s: pool error: %v", t.Slug, err)
			continue
		}
		store := crmstore.For(t.DesignVersion)
		q := index.NewQueue(pool)
		w := index.NewWorker(
			q,
			crmstore.NewRAGRecordLoader(store, pool),
			ai.NewOllamaDocEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel),
			ai.NewRagStore(pool),
		)
		go runTenantIndexWorker(ctx, t.Slug, w, q)
		go runTenantReconciliation(ctx, t.Slug, store, pool, q)
	}
}

// runTenantIndexWorker drains one tenant's rag_index_queue every 3s until ctx
// is cancelled (the goroutine's explicit exit strategy). Also publishes the
// tenant's queue-depth/oldest-pending-age metrics each tick — piggybacking on
// the existing cadence rather than adding a separate ticker.
func runTenantIndexWorker(ctx context.Context, slug string, w *index.Worker, q *index.Queue) {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := w.DrainOnce(ctx); err != nil {
				log.Printf("rag-index: tenant %s: drain error: %v", slug, err)
			}
			if pending, age, err := q.Stats(ctx); err != nil {
				log.Printf("rag-index: tenant %s: stats error: %v", slug, err)
			} else {
				metrics.SetRAGIndexQueueStats(slug, pending, age)
			}
		}
	}
}

// runTenantReconciliation runs reconcileTenantIndex immediately and then every
// 10 minutes until ctx is cancelled (the goroutine's explicit exit strategy).
// It is the backstop for the small write->enqueue crash window in
// crmstore.IndexingStore (see its doc comment).
func runTenantReconciliation(ctx context.Context, slug string, store crmstore.Store, pool *pgxpool.Pool, q *index.Queue) {
	reconcileTenantIndex(ctx, slug, store, pool, q)
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			reconcileTenantIndex(ctx, slug, store, pool, q)
		}
	}
}

// reconcileTenantIndex enqueues an upsert for every CRM record whose
// updated_at is newer than its rag_chunks vector (or has no chunk at all),
// closing the gap between a write committing and its enqueue job landing.
func reconcileTenantIndex(ctx context.Context, slug string, store crmstore.Store, pool *pgxpool.Pool, q *index.Queue) {
	rows, err := pool.Query(ctx, `SELECT source_id, updated_at FROM rag_chunks`)
	if err != nil {
		log.Printf("rag-reconcile: tenant %s: read rag_chunks failed: %v", slug, err)
		return
	}
	indexedAt := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			log.Printf("rag-reconcile: tenant %s: scan failed: %v", slug, err)
			return
		}
		indexedAt[id] = at
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("rag-reconcile: tenant %s: rows error: %v", slug, err)
		return
	}

	for _, key := range crmstore.CRMWorkflowKeys() {
		recs, err := store.ListRecords(ctx, pool, key, "all", "")
		if err != nil {
			log.Printf("rag-reconcile: tenant %s: list %s failed: %v", slug, key, err)
			continue
		}
		for _, rec := range recs {
			if at, ok := indexedAt[rec.ID]; ok && !rec.UpdatedAt.After(at) {
				continue // vector already current
			}
			if err := q.Enqueue(ctx, rec.ID, "upsert"); err != nil {
				log.Printf("rag-reconcile: tenant %s: enqueue %s failed: %v", slug, rec.ID, err)
			}
		}
	}
}

// seedPlatformOwner creates the platform-owner tenant and admin identity on
// first boot, then prints a one-time setup token to stdout. The token must be
// used with POST /api/platform/activate to set the admin password.
//
// Security model: the raw token is never stored or returned over HTTP.
// Only its SHA-256 hash is persisted. Reading the token requires access to
// server logs (Fly.io `fly logs`), which is the infra-level access gate.
//
// Idempotent: if an owner already exists, this is a silent no-op.
func seedPlatformOwner(ctx context.Context, cp *tenancy.ControlPlane) {
	email := config.AppConfig.PlatformAdminEmail
	if email == "" {
		return
	}

	// Already fully activated — nothing to do.
	if owner, err := cp.PlatformOwnerTenant(ctx); err == nil && owner != nil {
		if owner.Status == "active" {
			return
		}
		// Owner exists but isn't activated yet (token expired or server restarted).
		// Re-generate the setup token and print it again.
		identity, iErr := cp.AnyIdentityForTenant(ctx, owner.ID)
		if iErr != nil {
			log.Printf("WARN: seedPlatformOwner: re-seed: no identity: %v", iErr)
			return
		}
		printSetupToken(ctx, cp, identity.ID, identity.Email)
		return
	}

	slug := config.AppConfig.PlatformAdminSlug
	company := config.AppConfig.PlatformAdminCompany
	if slug == "" {
		slug = strings.ToLower(strings.ReplaceAll(company, " ", "-"))
	}
	if company == "" {
		company = slug
	}
	if slug == "" {
		log.Println("WARN: seedPlatformOwner: PLATFORM_ADMIN_SLUG or PLATFORM_ADMIN_COMPANY required")
		return
	}

	tenant, err := cp.CreateTenant(ctx, slug, company, true)
	if err != nil {
		log.Printf("WARN: seedPlatformOwner: create tenant: %v", err)
		return
	}

	identity, err := cp.CreateIdentity(ctx, tenant.ID, email, "", email, false)
	if err != nil {
		log.Printf("WARN: seedPlatformOwner: create identity: %v", err)
		return
	}

	printSetupToken(ctx, cp, identity.ID, email)
}

// printSetupToken generates a fresh one-time setup token, stores its SHA-256
// hash in the DB, and prints the raw token to stdout. Called on first boot and
// on restart when the owner exists but hasn't been activated yet.
func printSetupToken(ctx context.Context, cp *tenancy.ControlPlane, identityID, email string) {
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		log.Printf("WARN: printSetupToken: generate token: %v", err)
		return
	}
	rawToken := hex.EncodeToString(rawBytes)

	sum := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(sum[:])
	expiry := time.Now().Add(15 * time.Minute)
	if err := cp.SetIdentitySetupTokenHash(ctx, identityID, tokenHash, expiry); err != nil {
		log.Printf("WARN: printSetupToken: store token hash: %v", err)
		return
	}

	log.Println()
	log.Println("╔══════════════════════════════════════════════════════════════════╗")
	log.Println("║          STONESUITE — FIRST-TIME PLATFORM SETUP                 ║")
	log.Println("║                                                                  ║")
	log.Printf("║  Admin email  : %-48s  ║\n", email)
	log.Printf("║  Token expiry : %-48s  ║\n", expiry.UTC().Format("2006-01-02 15:04:05 UTC"))
	log.Println("║                                                                  ║")
	log.Println("║  curl -s -X POST http://localhost:8080/api/platform/activate \\  ║")
	log.Println("║    -H 'Content-Type: application/json' \\                        ║")
	log.Println("║    -d '{                                                         ║")
	log.Printf("║       \"token\":\"%s...\",  ║\n", rawToken[:32])
	log.Println("║       \"password\":\"<YOUR_PASSWORD>\"                              ║")
	log.Println("║    }'                                                            ║")
	log.Println("║                                                                  ║")
	log.Println("║  FULL TOKEN:                                                     ║")
	log.Printf("║  %s  ║\n", rawToken)
	log.Println("║                                                                  ║")
	log.Println("║  Restart the server if the token expires to get a new one.      ║")
	log.Println("╚══════════════════════════════════════════════════════════════════╝")
	log.Println()
}

// ensureOwnerWorkspace provisions the platform-owner tenant's database if it was
// never set up. The owner is a first-class tenant, so its members need a real
// workspace (seeded workflows/roles). Idempotent and non-fatal: any failure is
// logged and the server continues. Provisioning is asynchronous.
func ensureOwnerWorkspace(ctx context.Context, cp *tenancy.ControlPlane, p *provisioning.Provisioner) {
	if p == nil {
		return
	}
	owner, err := cp.PlatformOwnerTenant(ctx)
	if err != nil {
		log.Printf("owner-workspace bootstrap skipped: no platform-owner tenant (%v)", err)
		return
	}
	if owner.DBName != "" {
		return // already provisioned
	}
	// Do not provision a platform-owner tenant that hasn't been activated yet.
	// seedPlatformOwner creates the tenant in 'invited' status with no password;
	// the admin must call POST /api/platform/activate first, which sets status=active
	// and enqueues provisioning via the normal path.
	if owner.Status != "active" {
		log.Printf("owner-workspace bootstrap skipped: platform owner %q not yet activated (status=%s)", owner.Slug, owner.Status)
		return
	}
	identity, err := cp.AnyIdentityForTenant(ctx, owner.ID)
	if err != nil {
		log.Printf("owner-workspace bootstrap skipped: no identity for owner tenant %s (%v)", owner.Slug, err)
		return
	}
	log.Printf("Provisioning platform-owner workspace %q...", owner.Slug)
	p.Enqueue(provisioning.Job{
		TenantID:   owner.ID,
		Slug:       owner.Slug,
		IdentityID: identity.ID,
		Email:      identity.Email,
		FullName:   identity.FullName,
	})
}

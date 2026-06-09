package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"stonesuite-backend/config"
	"stonesuite-backend/controllers"
	"stonesuite-backend/database"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/provisioning"
	"stonesuite-backend/secret"
	"stonesuite-backend/tenancy"
)

func main() {
	// 1. Load Configurations and fail fast on insecure/invalid config.
	config.Load()
	if err := config.AppConfig.Validate(); err != nil {
		log.Fatalf("CRITICAL ERROR: %v", err)
	}

	// 2. Initialize the multi-tenant control plane (required).
	// When CONTROL_PLANE_DB_URL is set, requests can be resolved to a tenant and
	// routed to that tenant's isolated database. Until then, legacy routes work
	// unchanged so existing functionality is not disrupted during the migration.
	var resolver *tenancy.Resolver
	var tenantOps *controllers.TenantOps
	var userOps *controllers.UserOps
	var provisioner *provisioning.Provisioner
	if config.AppConfig.ControlPlaneDBURL != "" {
		cp, err := tenancy.NewControlPlane(context.Background(), config.AppConfig.ControlPlaneDBURL)
		if err != nil {
			log.Fatalf("CRITICAL ERROR: Failed to initialize control plane: %v", err)
		}

		// Auto-apply control-plane migrations on every startup (idempotent).
		// This creates tenants/identities/invites/etc. tables on a fresh Neon DB
		// and applies any new migrations on subsequent deploys.
		if err := database.ApplyControlPlaneMigrations(context.Background(), cp.Pool()); err != nil {
			log.Fatalf("CRITICAL ERROR: control-plane migrations failed: %v", err)
		}
		log.Println("Control-plane migrations: ok")

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

		// Provisioner (optional): requires an admin DSN to create tenant databases.
		if config.AppConfig.ProvisionAdminDBURL != "" {
			provider, perr := provisioning.NewSQLProvider(config.AppConfig.ProvisionAdminDBURL)
			if perr != nil {
				log.Fatalf("CRITICAL ERROR: invalid PROVISION_ADMIN_DB_URL: %v", perr)
			}
			provisioner = provisioning.New(cp, provider, cipher)
			provisioner.Start(2)
			log.Println("Tenant provisioner started (2 workers).")

			// Self-heal: the platform owner is a first-class tenant too. If its
			// workspace database was never provisioned (e.g. the owner was seeded
			// by hand), provision it now so platform admins get a working
			// workspace (workflows/roles) like any tenant.
			ensureOwnerWorkspace(context.Background(), cp, provisioner)

			// Fan-out migrations: apply any new tenant migrations (e.g. 000004
			// prospects, 000005 leads) to all already-provisioned tenant DBs.
			// Idempotent — skips tenants already at the latest version.
			go migrateAllTenants(context.Background(), cp, tenantRouter)
		} else {
			log.Println("Note: PROVISION_ADMIN_DB_URL not set — tenant provisioning disabled.")
		}

		tenantOps = controllers.NewTenantOps(cp, provisioner, tenantRouter)
		userOps = controllers.NewUserOps(cp, tenantRouter)
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

	// Multi-tenant routes (the control plane is always configured at this point).
	if tenantOps != nil {
		// Public: tenant-scoped login + password recovery.
		mux.HandleFunc("/api/auth/tenant-login", tenantOps.TenantLogin)
		mux.HandleFunc("POST /api/auth/forgot-password", tenantOps.ForgotPassword)
		mux.HandleFunc("GET /api/auth/reset-password/{token}", tenantOps.ValidateResetToken)
		mux.HandleFunc("POST /api/auth/reset-password", tenantOps.ResetPassword)

		// Public: password-reset flow (forgot → validate token → set new password).
		mux.HandleFunc("/api/auth/forgot-password", tenantOps.ForgotPassword)
		mux.HandleFunc("GET /api/auth/reset-password/{token}", tenantOps.ValidateResetToken)
		mux.HandleFunc("/api/auth/reset-password", tenantOps.ResetPassword)

		// Public: self-service onboarding (fill form → approval → set password).
		mux.HandleFunc("/api/onboarding/form-schema", tenantOps.FormSchema)
		mux.HandleFunc("/api/onboarding/apply/", tenantOps.GetApply) // GET /{token}
		mux.HandleFunc("/api/onboarding/apply", tenantOps.SubmitApply)
		mux.HandleFunc("/api/onboarding/set-password/", tenantOps.GetSetPassword) // GET /{token}
		mux.HandleFunc("/api/onboarding/set-password", tenantOps.SetPassword)

		// One-shot bootstrap (no auth — only works when no owner exists yet).
		mux.HandleFunc("/api/platform/bootstrap", tenantOps.Bootstrap)

		// Platform-admin: tenant management (auth required; admin checked inside).
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
		mux.Handle("/api/tenant/me", middleware.RequireAuth(resolver.Middleware(tenantMe)))

		// tenantChain applies RequireAuth → tenancy resolver before every handler.
		tenantChain := func(h http.HandlerFunc) http.Handler {
			return middleware.RequireAuth(resolver.Middleware(h))
		}

		// Tenant-scoped RBAC management (role editor API). Each handler runs
		// after RequireAuth + the tenancy resolver, then enforces the relevant
		// catalog permission (role:read / role:configure) per method.
		rbac := controllers.NewRBACOps()
		mux.Handle("/api/tenant/permissions/catalog", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.Catalog))))
		mux.Handle("/api/tenant/roles", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.Roles))))
		mux.Handle("/api/tenant/roles/", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.Role))))

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
		mux.Handle("GET /api/tenant/workflows/{id}/records", tenantChain(wf.ListRecords))
		mux.Handle("POST /api/tenant/workflows/{id}/records", tenantChain(wf.CreateRecord))
		mux.Handle("GET /api/tenant/records/{id}", tenantChain(wf.GetRecord))
		mux.Handle("PATCH /api/tenant/records/{id}", tenantChain(wf.UpdateRecord))
		mux.Handle("POST /api/tenant/records/{id}/transition", tenantChain(wf.TransitionRecord))

		// Dedicated CRM prospects table (migration 000004).
		ps := controllers.NewProspectOps()
		mux.Handle("GET /api/tenant/prospects", tenantChain(ps.ListProspects))
		mux.Handle("POST /api/tenant/prospects", tenantChain(ps.CreateProspect))
		mux.Handle("GET /api/tenant/prospects/{id}", tenantChain(ps.GetProspect))
		mux.Handle("PATCH /api/tenant/prospects/{id}", tenantChain(ps.UpdateProspect))
		mux.Handle("DELETE /api/tenant/prospects/{id}", tenantChain(ps.DeleteProspect))

		// Dedicated CRM leads table (migration 000005).
		ls := controllers.NewLeadOps()
		mux.Handle("GET /api/tenant/leads", tenantChain(ls.ListLeads))
		mux.Handle("POST /api/tenant/leads", tenantChain(ls.CreateLead))
		mux.Handle("GET /api/tenant/leads/{id}", tenantChain(ls.GetLead))
		mux.Handle("PATCH /api/tenant/leads/{id}", tenantChain(ls.UpdateLead))
		mux.Handle("DELETE /api/tenant/leads/{id}", tenantChain(ls.DeleteLead))
	}

	// 4. Global Middleware: CORS Policy Wrapper + Request Logger
	globalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log Request
		log.Printf("[%s] %s %s", time.Now().Format(time.RFC3339), r.Method, r.URL.Path)

		// Inject CORS Headers
		w.Header().Set("Access-Control-Allow-Origin", config.AppConfig.CorsOrigin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
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
		if path != "/api" && !strings.HasPrefix(path, "/api/auth/") && !strings.HasPrefix(path, "/api/onboarding") && !strings.HasPrefix(path, "/api/tenant") && !strings.HasPrefix(path, "/api/platform") {
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

	// 5. Start Server
	port := config.AppConfig.Port
	fmt.Println("===============================================")
	fmt.Println("  StoneSuite Go Login Backend is Running!      ")
	fmt.Printf("  Local Endpoint: http://localhost:%s\n", port)
	fmt.Printf("  Allowed CORS Origin: %s\n", config.AppConfig.CorsOrigin)
	fmt.Println("===============================================")

	server := &http.Server{
		Addr:         ":" + port,
		Handler:      globalHandler,
		WriteTimeout: 15 * time.Second,
		ReadTimeout:  15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("CRITICAL SERVER FAILURE: %v", err)
	}
}

// migrateAllTenants runs ApplyTenantMigrations on every provisioned tenant DB.
// Called in a goroutine on startup so new migrations (e.g. prospects, leads)
// are applied to existing tenant DBs without manual intervention.
func migrateAllTenants(ctx context.Context, cp *tenancy.ControlPlane, router *tenancy.Router) {
	tenants, err := cp.ListTenants(ctx)
	if err != nil {
		log.Printf("migrate-all: failed to list tenants: %v", err)
		return
	}
	latest, err := database.LatestTenantSchemaVersion()
	if err != nil {
		log.Printf("migrate-all: failed to read latest migration version: %v", err)
		return
	}
	for _, t := range tenants {
		if !t.Servable() {
			continue
		}
		if t.SchemaVersion >= latest {
			continue // already current
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
	}
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

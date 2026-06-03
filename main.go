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
	// 1. Load Configurations
	config.Load()

	// 2. Initialize PostgreSQL Database Service
	log.Println("Initializing PostgreSQL database service...")
	if err := database.InitPostgres(); err != nil {
		log.Fatalf("CRITICAL ERROR: Failed to initialize PostgreSQL database: %v", err)
	}
	log.Println("PostgreSQL database initialized successfully.")

	// 2b. Initialize the multi-tenant control plane (optional until configured).
	// When CONTROL_PLANE_DB_URL is set, requests can be resolved to a tenant and
	// routed to that tenant's isolated database. Until then, legacy routes work
	// unchanged so existing functionality is not disrupted during the migration.
	var resolver *tenancy.Resolver
	var tenantOps *controllers.TenantOps
	var provisioner *provisioning.Provisioner
	if config.AppConfig.ControlPlaneDBURL != "" {
		cp, err := tenancy.NewControlPlane(context.Background(), config.AppConfig.ControlPlaneDBURL)
		if err != nil {
			log.Fatalf("CRITICAL ERROR: Failed to initialize control plane: %v", err)
		}

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
		} else {
			log.Println("Note: PROVISION_ADMIN_DB_URL not set — tenant provisioning disabled.")
		}

		tenantOps = controllers.NewTenantOps(cp, provisioner)
		log.Println("Multi-tenant control plane initialized.")
	} else {
		log.Println("Note: CONTROL_PLANE_DB_URL not set — multi-tenant routes disabled (legacy mode).")
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

	// Mount Register & Login routes
	mux.HandleFunc("/api/auth/register", controllers.Register)
	mux.HandleFunc("/api/auth/login", controllers.Login)

	// Mount Password Reset & Email Verification routes
	mux.HandleFunc("/api/auth/forgot-password", controllers.ForgotPassword)
	mux.HandleFunc("/api/auth/reset-password", controllers.ResetPassword)
	mux.HandleFunc("/api/auth/verify-email", controllers.VerifyEmail)
	mux.HandleFunc("/api/auth/resend-verification", controllers.ResendVerification)

	// Mount OAuth callback routes
	mux.HandleFunc("/api/auth/entra/callback", controllers.EntraIDCallback)
	mux.HandleFunc("/api/auth/cognito/callback", controllers.CognitoCallback)

	// Mount Protected /me route using RequireAuth middleware
	meHandler := http.HandlerFunc(controllers.GetMe)
	mux.Handle("/api/auth/me", middleware.RequireAuth(meHandler))

	// Mount Customer Onboarding routes
	mux.Handle("/api/customers", middleware.RequireAuth(http.HandlerFunc(controllers.CustomersHandler)))
	mux.Handle("/api/customers/", middleware.RequireAuth(http.HandlerFunc(controllers.CustomerHandler)))
	mux.Handle("/api/invitations", middleware.RequireAuth(http.HandlerFunc(controllers.SendInvitation)))
	mux.HandleFunc("/api/onboarding/accept", controllers.CompleteOnboarding)
	mux.HandleFunc("/api/onboarding/submit", controllers.SubmitOnboarding)
	mux.HandleFunc("/api/onboarding/invite/", controllers.GetOnboardingInvite)

	// Mount CRM Lead routes
	mux.Handle("/api/leads", middleware.RequireAuth(http.HandlerFunc(controllers.LeadsHandler)))

	// Multi-tenant routes (only when the control plane is configured).
	if tenantOps != nil {
		// Public: tenant-scoped login + onboarding.
		mux.HandleFunc("/api/auth/tenant-login", tenantOps.TenantLogin)
		mux.HandleFunc("/api/onboarding/tenant-invite/", tenantOps.GetInvite)
		mux.HandleFunc("/api/onboarding/tenant-accept", tenantOps.AcceptInvite)

		// Platform-admin: tenant management (auth required; admin checked inside).
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

		// Tenant-scoped RBAC management (role editor API). Each handler runs
		// after RequireAuth + the tenancy resolver, then enforces the relevant
		// catalog permission (role:read / role:configure) per method.
		rbac := controllers.NewRBACOps()
		mux.Handle("/api/tenant/permissions/catalog", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.Catalog))))
		mux.Handle("/api/tenant/roles", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.Roles))))
		mux.Handle("/api/tenant/roles/", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.Role))))
		mux.Handle("/api/tenant/users/", middleware.RequireAuth(resolver.Middleware(http.HandlerFunc(rbac.UserRoles))))

		// Tenant-scoped workflow engine + records (Phase 3). Uses method+wildcard
		// routing; each handler enforces its own catalog permission. tenantChain
		// applies RequireAuth -> tenancy resolver before the handler.
		wf := controllers.NewWorkflowOps()
		tenantChain := func(h http.HandlerFunc) http.Handler {
			return middleware.RequireAuth(resolver.Middleware(h))
		}
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
		if path != "/api" && !strings.HasPrefix(path, "/api/auth/") && !strings.HasPrefix(path, "/api/customers") && !strings.HasPrefix(path, "/api/onboarding") && !strings.HasPrefix(path, "/api/invitations") && !strings.HasPrefix(path, "/api/leads") && !strings.HasPrefix(path, "/api/tenant") && !strings.HasPrefix(path, "/api/platform") {
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

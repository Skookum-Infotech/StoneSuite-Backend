package main

import (
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
		if path != "/api" && !strings.HasPrefix(path, "/api/auth/") {
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

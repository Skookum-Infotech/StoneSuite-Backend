package config

import (
	"encoding/base64"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	// Environment is "development" (default) or "production". Controls fail-fast
	// validation of security-critical secrets (see Validate).
	Environment            string
	Port                   string
	JWTSecret              string
	JWTExpiresIn           string
	JWTRememberMeExpiresIn string
	DBHost                 string
	DBPort                 string
	DBUser                 string
	DBPassword             string
	DBName                 string
	CorsOrigin             string
	FrontendURL            string
	// InviteExpiryHours is the default lifetime of an onboarding invite link.
	InviteExpiryHours int
	// Multi-tenant control plane
	ControlPlaneDBURL string // full DSN to the shared control-plane database
	// Neon provisioning (used when creating per-tenant databases)
	NeonAPIKey    string
	NeonProjectID string
	// Admin DSN with rights to CREATE/DROP tenant databases (e.g. .../postgres)
	ProvisionAdminDBURL string
	// Secret encryption: key (base64) used to encrypt tenant DB DSNs / SSO secrets at rest
	SecretEncryptionKey string
	// Microsoft Entra ID OAuth
	EntraIDClientID     string
	EntraIDClientSecret string
	EntraIDRedirectURI  string
	// AWS Cognito OAuth
	CognitoClientID     string
	CognitoClientSecret string
	CognitoDomain       string
	CognitoRedirectURI  string
	// Email Configuration
	SMTPHost       string
	SMTPPort       string
	SenderEmail    string
	SenderPassword string
}

var AppConfig Config

// Load loads all configuration variables from the .env file and/or environmental variables.
func Load() {
	// Try to find and load .env file
	// Find the current working directory to resolve paths
	cwd, err := os.Getwd()
	if err == nil {
		envPath := filepath.Join(cwd, ".env")
		if err := godotenv.Load(envPath); err != nil {
			log.Println("Note: .env file not found, using system environment variables")
		}
	}

	AppConfig = Config{
		Environment:            getEnv("APP_ENV", "development"),
		Port:                   getEnv("PORT", "8080"),
		// No default: an empty JWT secret is rejected by Validate(). A baked-in
		// default would let anyone forge tokens if the env var were ever unset.
		JWTSecret:              getEnv("JWT_SECRET", ""),
		JWTExpiresIn:           getEnv("JWT_EXPIRES_IN", "24h"),
		JWTRememberMeExpiresIn: getEnv("JWT_REMEMBER_ME_EXPIRES_IN", "720h"),
		CorsOrigin:             getEnv("CORS_ORIGIN", "http://localhost:5173"),
		FrontendURL:            getEnv("FRONTEND_URL", "http://localhost:5173"),
		InviteExpiryHours:      getEnvInt("INVITE_EXPIRY_HOURS", 24),
		// Multi-tenant control plane + provisioning
		ControlPlaneDBURL:   getEnv("CONTROL_PLANE_DB_URL", ""),
		NeonAPIKey:          getEnv("NEON_API_KEY", ""),
		NeonProjectID:       getEnv("NEON_PROJECT_ID", ""),
		ProvisionAdminDBURL: getEnv("PROVISION_ADMIN_DB_URL", ""),
		SecretEncryptionKey: getEnv("SECRET_ENCRYPTION_KEY", ""),
		// Microsoft Entra ID
		EntraIDClientID:     getEnv("ENTRA_ID_CLIENT_ID", ""),
		EntraIDClientSecret: getEnv("ENTRA_ID_CLIENT_SECRET", ""),
		EntraIDRedirectURI:  getEnv("ENTRA_ID_REDIRECT_URI", "http://localhost:8080/api/auth/entra/callback"),
		// AWS Cognito
		CognitoClientID:     getEnv("COGNITO_CLIENT_ID", ""),
		CognitoClientSecret: getEnv("COGNITO_CLIENT_SECRET", ""),
		CognitoDomain:       getEnv("COGNITO_DOMAIN", ""),
		CognitoRedirectURI:  getEnv("COGNITO_REDIRECT_URI", "http://localhost:8080/api/auth/cognito/callback"),
		// Email Configuration
		SMTPHost:       getEnv("SMTP_HOST", ""),
		SMTPPort:       getEnv("SMTP_PORT", "587"),
		SenderEmail:    getEnv("SENDER_EMAIL", ""),
		SenderPassword: getEnv("SENDER_PASSWORD", ""),
	}
}

// IsProduction reports whether the app is running in production mode (APP_ENV=production).
func (c Config) IsProduction() bool {
	return strings.EqualFold(c.Environment, "production")
}

// Validate checks security-critical configuration and returns an error describing
// every problem found. Call it immediately after Load and fail fast on error.
func (c Config) Validate() error {
	var problems []string

	// JWT secret is mandatory everywhere — without it tokens are unsigned/forgeable.
	if c.JWTSecret == "" {
		problems = append(problems, "JWT_SECRET is required (generate with: openssl rand -base64 48)")
	}

	// Secret encryption key: optional in dev (DSNs stored plaintext), mandatory in
	// production. When set, it must base64-decode to exactly 16/24/32 bytes.
	if c.SecretEncryptionKey == "" {
		if c.IsProduction() {
			problems = append(problems, "SECRET_ENCRYPTION_KEY is required in production (base64 of 32 random bytes: openssl rand -base64 32)")
		}
	} else {
		key, err := base64.StdEncoding.DecodeString(c.SecretEncryptionKey)
		if err != nil {
			problems = append(problems, "SECRET_ENCRYPTION_KEY is not valid base64")
		} else if n := len(key); n != 16 && n != 24 && n != 32 {
			problems = append(problems, fmt.Sprintf("SECRET_ENCRYPTION_KEY decodes to %d bytes, expected 16/24/32 (use: openssl rand -base64 32)", n))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

// getEnvInt reads an integer env var, falling back to defaultValue when unset or invalid.
func getEnvInt(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	return defaultValue
}

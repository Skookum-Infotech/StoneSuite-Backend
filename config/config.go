package config

import (
	"log"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

type Config struct {
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
		Port:                   getEnv("PORT", "8080"),
		JWTSecret:              getEnv("JWT_SECRET", "stone_suite_go_backend_default_secret_key_change_me_in_prod"),
		JWTExpiresIn:           getEnv("JWT_EXPIRES_IN", "24h"),
		JWTRememberMeExpiresIn: getEnv("JWT_REMEMBER_ME_EXPIRES_IN", "720h"),
		CorsOrigin:             getEnv("CORS_ORIGIN", "http://localhost:5173"),
		FrontendURL:            getEnv("FRONTEND_URL", "http://localhost:5173"),
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

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

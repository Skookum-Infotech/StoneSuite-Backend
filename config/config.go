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
	DBFilePath             string
	CorsOrigin             string
	// Microsoft Entra ID OAuth
	EntraIDClientID     string
	EntraIDClientSecret string
	EntraIDRedirectURI  string
	// AWS Cognito OAuth
	CognitoClientID     string
	CognitoClientSecret string
	CognitoDomain       string
	CognitoRedirectURI  string
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
		DBFilePath:             getEnv("DB_FILE_PATH", "./data/users.json"),
		CorsOrigin:             getEnv("CORS_ORIGIN", "http://localhost:5173"),
		// Microsoft Entra ID
		EntraIDClientID:     getEnv("ENTRA_ID_CLIENT_ID", ""),
		EntraIDClientSecret: getEnv("ENTRA_ID_CLIENT_SECRET", ""),
		EntraIDRedirectURI:  getEnv("ENTRA_ID_REDIRECT_URI", "http://localhost:8080/api/auth/entra/callback"),
		// AWS Cognito
		CognitoClientID:     getEnv("COGNITO_CLIENT_ID", ""),
		CognitoClientSecret: getEnv("COGNITO_CLIENT_SECRET", ""),
		CognitoDomain:       getEnv("COGNITO_DOMAIN", ""),
		CognitoRedirectURI:  getEnv("COGNITO_REDIRECT_URI", "http://localhost:8080/api/auth/cognito/callback"),
	}
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

package database

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"stonesuite-backend/config"
	"stonesuite-backend/models"

	_ "github.com/lib/pq"
)

var DB *sql.DB

// Init initializes the PostgreSQL connection and ensures the users table exists.
func Init() error {
	dsn := fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		config.AppConfig.DBHost,
		config.AppConfig.DBPort,
		config.AppConfig.DBUser,
		config.AppConfig.DBPassword,
		config.AppConfig.DBName,
		config.AppConfig.DBSSLMode,
	)

	var err error
	DB, err = sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("failed to open database connection: %w", err)
	}

	DB.SetMaxOpenConns(25)
	DB.SetMaxIdleConns(5)
	DB.SetConnMaxLifetime(5 * time.Minute)

	if err = DB.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	log.Println("Connected to PostgreSQL successfully")

	if err = runMigrations(); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	return nil
}

func runMigrations() error {
	query := `
	CREATE TABLE IF NOT EXISTS users (
		id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
		email VARCHAR(255) UNIQUE NOT NULL,
		password_hash TEXT,
		full_name VARCHAR(255) NOT NULL,
		oauth_provider VARCHAR(50),
		oauth_id TEXT,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	CREATE INDEX IF NOT EXISTS idx_users_email ON users(email);
	CREATE INDEX IF NOT EXISTS idx_users_oauth ON users(oauth_provider, oauth_id);
	`
	_, err := DB.Exec(query)
	return err
}

// GetAllUsers retrieves all user accounts.
func GetAllUsers() ([]models.User, error) {
	query := `SELECT id, email, COALESCE(password_hash, ''), full_name,
		COALESCE(oauth_provider, ''), COALESCE(oauth_id, ''), created_at, updated_at
		FROM users ORDER BY created_at DESC`

	rows, err := DB.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []models.User
	for rows.Next() {
		var u models.User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.FullName,
			&u.OAuthProvider, &u.OAuthID, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		users = append(users, u)
	}

	return users, rows.Err()
}

// GetUserByEmail finds a user by their email address (case-insensitive).
func GetUserByEmail(email string) (*models.User, error) {
	query := `SELECT id, email, COALESCE(password_hash, ''), full_name,
		COALESCE(oauth_provider, ''), COALESCE(oauth_id, ''), created_at, updated_at
		FROM users WHERE LOWER(email) = LOWER($1)`

	var u models.User
	err := DB.QueryRow(query, strings.TrimSpace(email)).Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.FullName,
		&u.OAuthProvider, &u.OAuthID, &u.CreatedAt, &u.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUserByID finds a user by their ID.
func GetUserByID(id string) (*models.User, error) {
	query := `SELECT id, email, COALESCE(password_hash, ''), full_name,
		COALESCE(oauth_provider, ''), COALESCE(oauth_id, ''), created_at, updated_at
		FROM users WHERE id = $1`

	var u models.User
	err := DB.QueryRow(query, id).Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.FullName,
		&u.OAuthProvider, &u.OAuthID, &u.CreatedAt, &u.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser registers a new user with email, hashed password, and full name.
func CreateUser(email, passwordHash, fullName string) (*models.User, error) {
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))

	query := `INSERT INTO users (email, password_hash, full_name)
		VALUES ($1, $2, $3)
		RETURNING id, email, COALESCE(password_hash, ''), full_name,
			COALESCE(oauth_provider, ''), COALESCE(oauth_id, ''), created_at, updated_at`

	var u models.User
	err := DB.QueryRow(query, normalizedEmail, passwordHash, strings.TrimSpace(fullName)).Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.FullName,
		&u.OAuthProvider, &u.OAuthID, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, fmt.Errorf("user with this email already exists")
		}
		return nil, err
	}

	log.Printf("Successfully created and saved user: %s", u.Email)
	return &u, nil
}

// UpsertOAuthUser creates or updates a user authenticated via OAuth provider.
func UpsertOAuthUser(email, fullName, provider, providerID string) (*models.User, error) {
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))

	query := `INSERT INTO users (email, full_name, oauth_provider, oauth_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (email) DO UPDATE SET
			full_name = EXCLUDED.full_name,
			oauth_provider = EXCLUDED.oauth_provider,
			oauth_id = EXCLUDED.oauth_id,
			updated_at = NOW()
		RETURNING id, email, COALESCE(password_hash, ''), full_name,
			COALESCE(oauth_provider, ''), COALESCE(oauth_id, ''), created_at, updated_at`

	var u models.User
	err := DB.QueryRow(query, normalizedEmail, strings.TrimSpace(fullName), provider, providerID).Scan(
		&u.ID, &u.Email, &u.PasswordHash, &u.FullName,
		&u.OAuthProvider, &u.OAuthID, &u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	log.Printf("Successfully upserted OAuth user: %s (provider: %s)", u.Email, provider)
	return &u, nil
}

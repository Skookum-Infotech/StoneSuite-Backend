package database

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"stonesuite-backend/config"
	"stonesuite-backend/models"
)

var (
	mu           sync.RWMutex
	initialized  bool
	resolvedPath string
)

// Init resolves the DB filepath and creates the parent folder and empty file if missing.
func InitJSON() error {
	mu.Lock()
	defer mu.Unlock()

	if initialized {
		return nil
	}

	dbPath := config.AppConfig.DBFilePath
	// Resolve relative or absolute path based on CWD
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current working directory: %w", err)
	}

	if filepath.IsAbs(dbPath) {
		resolvedPath = dbPath
	} else {
		resolvedPath = filepath.Join(cwd, dbPath)
	}

	dir := filepath.Dir(resolvedPath)

	// Create directory if not exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}

	// Create file with empty array if not exists
	if _, err := os.Stat(resolvedPath); os.IsNotExist(err) {
		emptyDB := []models.User{}
		data, err := json.MarshalIndent(emptyDB, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal empty database: %w", err)
		}

		if err := os.WriteFile(resolvedPath, data, 0644); err != nil {
			return fmt.Errorf("failed to write initial empty database: %w", err)
		}
	}

	initialized = true
	return nil
}
	mu.Lock()
	defer mu.Unlock()

	if initialized {
		return nil
	}

	dbPath := config.AppConfig.DBFilePath
	// Resolve relative or absolute path based on CWD
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current working directory: %w", err)
	}

	if filepath.IsAbs(dbPath) {
		resolvedPath = dbPath
	} else {
		resolvedPath = filepath.Join(cwd, dbPath)
	}

	dir := filepath.Dir(resolvedPath)

	// Create directory if not exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create database directory: %w", err)
	}

	// Create file with empty array if not exists
	if _, err := os.Stat(resolvedPath); os.IsNotExist(err) {
		emptyDB := []models.User{}
		data, err := json.MarshalIndent(emptyDB, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal empty database: %w", err)
		}

		if err := os.WriteFile(resolvedPath, data, 0644); err != nil {
			return fmt.Errorf("failed to write initial empty database: %w", err)
		}
	}

	initialized = true
	return nil
}

// readUsersRaw reads from disk. Caller MUST hold RLock or Lock.
func readUsersRaw() ([]models.User, error) {
	file, err := os.Open(resolvedPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	var users []models.User
	if err := json.Unmarshal(data, &users); err != nil {
		return nil, err
	}

	return users, nil
}

// writeUsersRaw writes back to disk. Caller MUST hold Lock.
func writeUsersRaw(users []models.User) error {
	data, err := json.MarshalIndent(users, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(resolvedPath, data, 0644)
}

// GetAllUsers retrieves all user accounts.
func GetAllUsers() ([]models.User, error) {
	if err := Init(); err != nil {
		return nil, err
	}

	mu.RLock()
	defer mu.RUnlock()

	return readUsersRaw()
}

// GetUserByEmail finds a user by their email address (case-insensitive).
func GetUserByEmailJSON(email string) (*models.User, error) {
	if err := InitJSON(); err != nil {
		return nil, err
	}

	mu.RLock()
	defer mu.RUnlock()

	users, err := readUsersRaw()
	if err != nil {
		return nil, err
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	for _, u := range users {
		if strings.ToLower(strings.TrimSpace(u.Email)) == normalizedEmail {
			return &u, nil
		}
	}

	return nil, nil // Not found
}
	if err := Init(); err != nil {
		return nil, err
	}

	mu.RLock()
	defer mu.RUnlock()

	users, err := readUsersRaw()
	if err != nil {
		return nil, err
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	for _, u := range users {
		if strings.ToLower(strings.TrimSpace(u.Email)) == normalizedEmail {
			return &u, nil
		}
	}

	return nil, nil // Not found
}

// GetUserByID finds a user by their ID.
func GetUserByIDJSON(id string) (*models.User, error) {
	if err := InitJSON(); err != nil {
		return nil, err
	}

	mu.RLock()
	defer mu.RUnlock()

	users, err := readUsersRaw()
	if err != nil {
		return nil, err
	}

	for _, u := range users {
		if u.ID == id {
			return &u, nil
		}
	}

	return nil, nil // Not found
}
	if err := Init(); err != nil {
		return nil, err
	}

	mu.RLock()
	defer mu.RUnlock()

	users, err := readUsersRaw()
	if err != nil {
		return nil, err
	}

	for _, u := range users {
		if u.ID == id {
			return &u, nil
		}
	}

	return nil, nil // Not found
}

// CreateUser registers a new user with email, hashed password, and full name.
func CreateUserJSON(email, passwordHash, fullName string) (*models.User, error) {
	if err := InitJSON(); err != nil {
		return nil, err
	}

	mu.Lock()
	defer mu.Unlock()

	users, err := readUsersRaw()
	if err != nil {
		return nil, err
	}

	// Double-check duplicates under full Lock to prevent race condition
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	for _, u := range users {
		if strings.ToLower(strings.TrimSpace(u.Email)) == normalizedEmail {
			return nil, errors.New("user with this email already exists")
		}
	}

	newUser := models.User{
		ID:           generateUUID(),
		Email:        normalizedEmail,
		PasswordHash: passwordHash,
		FullName:     strings.TrimSpace(fullName),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	users = append(users, newUser)
	if err := writeUsersRaw(users); err != nil {
		return nil, err
	}

	log.Printf("Successfully created and saved user: %s", newUser.Email)
	return &newUser, nil
}
	if err := Init(); err != nil {
		return nil, err
	}

	mu.Lock()
	defer mu.Unlock()

	users, err := readUsersRaw()
	if err != nil {
		return nil, err
	}

	// Double-check duplicates under full Lock to prevent race condition
	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	for _, u := range users {
		if strings.ToLower(strings.TrimSpace(u.Email)) == normalizedEmail {
			return nil, errors.New("user with this email already exists")
		}
	}

	newUser := models.User{
		ID:           generateUUID(),
		Email:        normalizedEmail,
		PasswordHash: passwordHash,
		FullName:     strings.TrimSpace(fullName),
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}

	users = append(users, newUser)
	if err := writeUsersRaw(users); err != nil {
		return nil, err
	}

	log.Printf("Successfully created and saved user: %s", newUser.Email)
	return &newUser, nil
}

// generateUUID creates a cryptographically secure UUID v4.
func generateUUID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback to timestamp + random string if rand fails
		return fmt.Sprintf("u-%d-%s", time.Now().UnixNano(), strings.ReplaceAll(time.Now().Format("15-04-05.000"), ".", ""))
	}

	// RFC 4122 Variant and Version settings
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant 10

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// UpsertOAuthUser creates or updates a user authenticated via OAuth provider.
// If the user already exists by email, it updates their OAuth info.
// If they don't exist, it creates a new user record.
func UpsertOAuthUser(email, fullName, provider, providerID string) (*models.User, error) {
	if err := Init(); err != nil {
		return nil, err
	}

	mu.Lock()
	defer mu.Unlock()

	users, err := readUsersRaw()
	if err != nil {
		return nil, err
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))

	// Check if user exists by email
	for i, u := range users {
		if strings.ToLower(strings.TrimSpace(u.Email)) == normalizedEmail {
			// Update existing user
			users[i].FullName = strings.TrimSpace(fullName)
			users[i].OAuthProvider = provider
			users[i].OAuthID = providerID
			users[i].UpdatedAt = time.Now()

			if err := writeUsersRaw(users); err != nil {
				return nil, err
			}

			log.Printf("Successfully updated OAuth user: %s (provider: %s)", users[i].Email, provider)
			return &users[i], nil
		}
	}

	// Create new user
	newUser := models.User{
		ID:            generateUUID(),
		Email:         normalizedEmail,
		FullName:      strings.TrimSpace(fullName),
		OAuthProvider: provider,
		OAuthID:       providerID,
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}

	users = append(users, newUser)
	if err := writeUsersRaw(users); err != nil {
		return nil, err
	}

	log.Printf("Successfully created new OAuth user: %s (provider: %s)", newUser.Email, provider)
	return &newUser, nil
}

// IncrementFailedLoginAttempts increments failed login counter for a user.
func IncrementFailedLoginAttempts(email string) (*models.User, error) {
	if err := Init(); err != nil {
		return nil, err
	}

	mu.Lock()
	defer mu.Unlock()

	users, err := readUsersRaw()
	if err != nil {
		return nil, err
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	for i, u := range users {
		if strings.ToLower(strings.TrimSpace(u.Email)) == normalizedEmail {
			users[i].FailedLoginAttempts++

			// Lock account after 3 failed attempts for 15 minutes
			if users[i].FailedLoginAttempts >= 3 {
				users[i].IsLocked = true
				users[i].LockedUntil = time.Now().Add(15 * time.Minute)
			}

			users[i].UpdatedAt = time.Now()

			if err := writeUsersRaw(users); err != nil {
				return nil, err
			}

			log.Printf("Failed login attempt for user: %s (attempts: %d)", users[i].Email, users[i].FailedLoginAttempts)
			return &users[i], nil
		}
	}

	return nil, errors.New("user not found")
}

// ResetFailedLoginAttempts resets the failed login counter.
func ResetFailedLoginAttempts(email string) error {
	if err := Init(); err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	users, err := readUsersRaw()
	if err != nil {
		return err
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	for i, u := range users {
		if strings.ToLower(strings.TrimSpace(u.Email)) == normalizedEmail {
			users[i].FailedLoginAttempts = 0
			users[i].IsLocked = false
			users[i].LockedUntil = time.Time{}
			users[i].UpdatedAt = time.Now()

			if err := writeUsersRaw(users); err != nil {
				return err
			}

			log.Printf("Reset failed login attempts for user: %s", users[i].Email)
			return nil
		}
	}

	return errors.New("user not found")
}

// SetPasswordResetToken saves a password reset token for the user.
func SetPasswordResetToken(email string, token string, expiryMinutes int) error {
	if err := Init(); err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	users, err := readUsersRaw()
	if err != nil {
		return err
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	for i, u := range users {
		if strings.ToLower(strings.TrimSpace(u.Email)) == normalizedEmail {
			users[i].PasswordResetToken = token
			users[i].PasswordResetExpiry = time.Now().Add(time.Duration(expiryMinutes) * time.Minute)
			users[i].UpdatedAt = time.Now()

			if err := writeUsersRaw(users); err != nil {
				return err
			}

			log.Printf("Password reset token set for user: %s", users[i].Email)
			return nil
		}
	}

	return errors.New("user not found")
}

// GetUserByPasswordResetToken finds a user by their password reset token.
func GetUserByPasswordResetToken(token string) (*models.User, error) {
	if err := Init(); err != nil {
		return nil, err
	}

	mu.RLock()
	defer mu.RUnlock()

	users, err := readUsersRaw()
	if err != nil {
		return nil, err
	}

	for _, u := range users {
		if u.PasswordResetToken == token && time.Now().Before(u.PasswordResetExpiry) {
			return &u, nil
		}
	}

	return nil, errors.New("invalid or expired reset token")
}

// UpdatePassword updates the user's password hash and clears reset token.
func UpdatePassword(email string, newPasswordHash string) error {
	if err := Init(); err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	users, err := readUsersRaw()
	if err != nil {
		return err
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	for i, u := range users {
		if strings.ToLower(strings.TrimSpace(u.Email)) == normalizedEmail {
			users[i].PasswordHash = newPasswordHash
			users[i].PasswordResetToken = ""
			users[i].PasswordResetExpiry = time.Time{}
			users[i].UpdatedAt = time.Now()

			if err := writeUsersRaw(users); err != nil {
				return err
			}

			log.Printf("Password updated for user: %s", users[i].Email)
			return nil
		}
	}

	return errors.New("user not found")
}

// SetEmailVerificationCode saves email verification code for a user.
func SetEmailVerificationCode(email string, code string) error {
	if err := Init(); err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	users, err := readUsersRaw()
	if err != nil {
		return err
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	for i, u := range users {
		if strings.ToLower(strings.TrimSpace(u.Email)) == normalizedEmail {
			users[i].EmailVerificationCode = code
			users[i].UpdatedAt = time.Now()

			if err := writeUsersRaw(users); err != nil {
				return err
			}

			log.Printf("Email verification code set for user: %s", users[i].Email)
			return nil
		}
	}

	return errors.New("user not found")
}

// VerifyEmail marks the user's email as verified.
func VerifyEmail(email string, code string) error {
	if err := Init(); err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	users, err := readUsersRaw()
	if err != nil {
		return err
	}

	normalizedEmail := strings.ToLower(strings.TrimSpace(email))
	for i, u := range users {
		if strings.ToLower(strings.TrimSpace(u.Email)) == normalizedEmail {
			if u.EmailVerificationCode != code {
				return errors.New("invalid verification code")
			}

			users[i].EmailVerified = true
			users[i].EmailVerificationCode = ""
			users[i].UpdatedAt = time.Now()

			if err := writeUsersRaw(users); err != nil {
				return err
			}

			log.Printf("Email verified for user: %s", users[i].Email)
			return nil
		}
	}

	return errors.New("user not found")
}

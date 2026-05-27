package database

import (
    "context"
    "errors"
    "fmt"
    "log"
    "os"
    "strings"
    "time"

    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"
    "stonesuite-backend/config"
    "stonesuite-backend/models"
)

var pgPool *pgxpool.Pool

// InitPostgres creates a connection pool using environment variables (or config).
func InitPostgres() error {
    // Resolve connection parameters – fall back to config defaults if env not set.
    host := getEnv("DB_HOST", config.AppConfig.DBHost)
    port := getEnv("DB_PORT", config.AppConfig.DBPort)
    user := getEnv("DB_USER", config.AppConfig.DBUser)
    password := getEnv("DB_PASSWORD", config.AppConfig.DBPassword)
    dbname := getEnv("DB_NAME", config.AppConfig.DBName)

    // Build DSN (PostgreSQL URI)
    dsn := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, password, host, port, dbname)
    cfg, err := pgxpool.ParseConfig(dsn)
    if err != nil {
        return fmt.Errorf("parse pgx config: %w", err)
    }
    // Tune pool (reasonable defaults)
    cfg.MaxConns = 10
    cfg.MaxConnLifetime = time.Hour

    pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
    if err != nil {
        return fmt.Errorf("create pgx pool: %w", err)
    }
    pgPool = pool
    log.Println("PostgreSQL connection pool established")
    return nil
}

func getEnv(key, fallback string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return fallback
}

// ----- CRUD helpers -----

// scanUser reads the standard 15-column user row (with COALESCE on nullable strings).
// Nullable timestamps (locked_until, password_reset_expiry) are scanned into *time.Time.
func scanUser(row interface{ Scan(...any) error }) (*models.User, error) {
    var u models.User
    var lockedUntil *time.Time
    var resetExpiry *time.Time
    err := row.Scan(
        &u.ID, &u.Email, &u.PasswordHash, &u.FullName,
        &u.OAuthProvider, &u.OAuthID,
        &u.FailedLoginAttempts, &u.IsLocked, &lockedUntil, &u.EmailVerified,
        &u.EmailVerificationCode, &u.PasswordResetToken, &resetExpiry,
        &u.CreatedAt, &u.UpdatedAt,
    )
    if errors.Is(err, pgx.ErrNoRows) {
        return nil, nil
    }
    if err != nil {
        return nil, err
    }
    if lockedUntil != nil {
        u.LockedUntil = *lockedUntil
    }
    if resetExpiry != nil {
        u.PasswordResetExpiry = *resetExpiry
    }
    return &u, nil
}

func GetUserByEmail(email string) (*models.User, error) {
    if pgPool == nil {
        if err := InitPostgres(); err != nil {
            return nil, err
        }
    }
    row := pgPool.QueryRow(context.Background(),
        `SELECT id, email, COALESCE(password_hash,''), full_name,
                COALESCE(oauth_provider,''), COALESCE(oauth_id,''),
                failed_login_attempts, is_locked, locked_until, email_verified,
                COALESCE(email_verification_code,''), COALESCE(password_reset_token,''),
                password_reset_expiry, created_at, updated_at
         FROM users WHERE LOWER(email)=LOWER($1)`, email)
    return scanUser(row)
}

func GetUserByID(id string) (*models.User, error) {
    if pgPool == nil {
        if err := InitPostgres(); err != nil {
            return nil, err
        }
    }
    row := pgPool.QueryRow(context.Background(),
        `SELECT id, email, COALESCE(password_hash,''), full_name,
                COALESCE(oauth_provider,''), COALESCE(oauth_id,''),
                failed_login_attempts, is_locked, locked_until, email_verified,
                COALESCE(email_verification_code,''), COALESCE(password_reset_token,''),
                password_reset_expiry, created_at, updated_at
         FROM users WHERE id=$1`, id)
    return scanUser(row)
}

func CreateUser(email, passwordHash, fullName string) (*models.User, error) {
    if pgPool == nil {
        if err := InitPostgres(); err != nil {
            return nil, err
        }
    }
    normalized := strings.ToLower(strings.TrimSpace(email))
    // Insert and return the generated ID
    var id string
    err := pgPool.QueryRow(context.Background(),
        `INSERT INTO users (email, password_hash, full_name, created_at, updated_at)
         VALUES ($1, $2, $3, NOW(), NOW()) RETURNING id`,
        normalized, passwordHash, strings.TrimSpace(fullName)).Scan(&id)
    if err != nil {
        return nil, err
    }
    user := &models.User{ID: id, Email: normalized, PasswordHash: passwordHash, FullName: strings.TrimSpace(fullName), CreatedAt: time.Now(), UpdatedAt: time.Now()}
    return user, nil
}

func UpdatePassword(email string, newHash string) error {
    if pgPool == nil {
        if err := InitPostgres(); err != nil {
            return err
        }
    }
    _, err := pgPool.Exec(context.Background(),
        `UPDATE users SET password_hash=$1, password_reset_token=NULL, password_reset_expiry=NULL, updated_at=NOW() WHERE LOWER(email)=LOWER($2)`,
        newHash, email)
    return err
}

func IncrementFailedLoginAttempts(email string) (*models.User, error) {
    if pgPool == nil {
        if err := InitPostgres(); err != nil {
            return nil, err
        }
    }
    // Increment counter atomically and return the updated row
    var u models.User
    err := pgPool.QueryRow(context.Background(),
        `UPDATE users SET failed_login_attempts = failed_login_attempts + 1, updated_at = NOW()
         WHERE LOWER(email)=LOWER($1)
         RETURNING id, email, password_hash, full_name, failed_login_attempts, is_locked, locked_until, created_at, updated_at`,
        email).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.FullName, &u.FailedLoginAttempts, &u.IsLocked, &u.LockedUntil, &u.CreatedAt, &u.UpdatedAt)
    if err != nil {
        return nil, err
    }
    // Lock account if attempts exceed threshold
    if u.FailedLoginAttempts >= 3 {
        _, err = pgPool.Exec(context.Background(), `UPDATE users SET is_locked=TRUE, locked_until=NOW() + INTERVAL '15 minutes' WHERE id=$1`, u.ID)
        if err != nil {
            return nil, err
        }
        u.IsLocked = true
        u.LockedUntil = time.Now().Add(15 * time.Minute)
    }
    return &u, nil
}

func ResetFailedLoginAttempts(email string) error {
    if pgPool == nil {
        if err := InitPostgres(); err != nil {
            return err
        }
    }
    _, err := pgPool.Exec(context.Background(),
        `UPDATE users SET failed_login_attempts=0, is_locked=FALSE, locked_until=NULL, updated_at=NOW() WHERE LOWER(email)=LOWER($1)`, email)
    return err
}

func SetPasswordResetToken(email, token string, expiryMinutes int) error {
    if pgPool == nil {
        if err := InitPostgres(); err != nil {
            return err
        }
    }
    expiry := time.Now().Add(time.Duration(expiryMinutes) * time.Minute)
    _, err := pgPool.Exec(context.Background(),
        `UPDATE users SET password_reset_token=$1, password_reset_expiry=$2, updated_at=NOW() WHERE LOWER(email)=LOWER($3)`, token, expiry, email)
    return err
}

func GetUserByPasswordResetToken(token string) (*models.User, error) {
    if pgPool == nil {
        if err := InitPostgres(); err != nil {
            return nil, err
        }
    }
    row := pgPool.QueryRow(context.Background(),
        `SELECT id, email, COALESCE(password_hash,''), full_name,
                COALESCE(password_reset_token,''), password_reset_expiry, created_at, updated_at
         FROM users WHERE password_reset_token=$1 AND password_reset_expiry > NOW()`, token)
    var u models.User
    var resetExpiry *time.Time
    err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.FullName, &u.PasswordResetToken, &resetExpiry, &u.CreatedAt, &u.UpdatedAt)
    if errors.Is(err, pgx.ErrNoRows) {
        return nil, nil
    }
    if err != nil {
        return nil, err
    }
    if resetExpiry != nil {
        u.PasswordResetExpiry = *resetExpiry
    }
    return &u, nil
}

func SetEmailVerificationCode(email, code string) error {
    if pgPool == nil {
        if err := InitPostgres(); err != nil {
            return err
        }
    }
    _, err := pgPool.Exec(context.Background(),
        `UPDATE users SET email_verification_code=$1, updated_at=NOW() WHERE LOWER(email)=LOWER($2)`, code, email)
    return err
}

func VerifyEmail(email, code string) error {
    if pgPool == nil {
        if err := InitPostgres(); err != nil {
            return err
        }
    }
    // Validate code and mark verified atomically
    result, err := pgPool.Exec(context.Background(),
        `UPDATE users SET email_verified=TRUE, email_verification_code=NULL, updated_at=NOW()
         WHERE LOWER(email)=LOWER($1) AND email_verification_code=$2`, email, code)
    if err != nil {
        return err
    }
    if result.RowsAffected() == 0 {
        return fmt.Errorf("invalid verification code")
    }
    return nil
}

func UpsertOAuthUser(email, fullName, provider, providerID string) (*models.User, error) {
    if pgPool == nil {
        if err := InitPostgres(); err != nil {
            return nil, err
        }
    }
    normalized := strings.ToLower(strings.TrimSpace(email))
    // Try to update existing row
    var id string
    err := pgPool.QueryRow(context.Background(),
        `UPDATE users SET full_name=$1, oauth_provider=$2, oauth_id=$3, updated_at=NOW()
         WHERE email=$4 RETURNING id`, strings.TrimSpace(fullName), provider, providerID, normalized).Scan(&id)
    if err == nil {
        // Updated existing user
        return GetUserByID(id)
    }
    // Insert new user
    err = pgPool.QueryRow(context.Background(),
        `INSERT INTO users (email, full_name, oauth_provider, oauth_id, created_at, updated_at)
         VALUES ($1, $2, $3, $4, NOW(), NOW()) RETURNING id`,
        normalized, strings.TrimSpace(fullName), provider, providerID).Scan(&id)
    if err != nil {
        return nil, err
    }
    return GetUserByID(id)
}

// Additional helper: wrapper Init for compatibility with existing JSON code.
func Init() error {
    return InitPostgres()
}


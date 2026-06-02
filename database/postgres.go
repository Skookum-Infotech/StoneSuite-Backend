package database

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"stonesuite-backend/config"
	"stonesuite-backend/models"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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


func GetAllCustomers() ([]models.Customer, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}

	rows, err := pgPool.Query(context.Background(),
		`SELECT id, name, COALESCE(legal_name,''), COALESCE(industry,''), COALESCE(website,''),
		        COALESCE(country,''), COALESCE(currency,''), COALESCE(timezone,''), COALESCE(tax_id,''),
		        COALESCE(billing_address,''), COALESCE(shipping_address,''), COALESCE(return_address,''),
		        status, created_at, updated_at
         FROM customers ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var customers []models.Customer
	for rows.Next() {
		var c models.Customer
		if err := rows.Scan(&c.ID, &c.Name, &c.LegalName, &c.Industry, &c.Website,
			&c.Country, &c.Currency, &c.Timezone, &c.TaxID,
			&c.BillingAddress, &c.ShippingAddress, &c.ReturnAddress,
			&c.Status, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		customers = append(customers, c)
	}

	if rows.Err() != nil {
		return nil, rows.Err()
	}

	for i := range customers {
		contacts, err := GetCustomerContacts(customers[i].ID)
		if err != nil {
			return nil, err
		}
		customers[i].Contacts = contacts
	}

	return customers, nil
}

func GetCustomerByIDWithContacts(id string) (*models.Customer, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}

	row := pgPool.QueryRow(context.Background(),
		`SELECT id, name, COALESCE(legal_name,''), COALESCE(industry,''), COALESCE(website,''),
		        COALESCE(country,''), COALESCE(currency,''), COALESCE(timezone,''), COALESCE(tax_id,''),
		        COALESCE(billing_address,''), COALESCE(shipping_address,''), COALESCE(return_address,''),
		        status, created_at, updated_at
         FROM customers WHERE id=$1`, id)
	var c models.Customer
	if err := row.Scan(&c.ID, &c.Name, &c.LegalName, &c.Industry, &c.Website,
		&c.Country, &c.Currency, &c.Timezone, &c.TaxID,
		&c.BillingAddress, &c.ShippingAddress, &c.ReturnAddress,
		&c.Status, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}

	contacts, err := GetCustomerContacts(c.ID)
	if err != nil {
		return nil, err
	}
	c.Contacts = contacts
	return &c, nil
}

func CreateCustomer(name, legalName, industry, website, country, currency, timezone, taxID, billingAddress, shippingAddress, returnAddress string) (*models.Customer, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}
	id, err := NewUUID()
	if err != nil {
		return nil, err
	}

	_, err = pgPool.Exec(context.Background(),
		`INSERT INTO customers (id, name, legal_name, industry, website, country, currency, timezone, tax_id,
		                        billing_address, shipping_address, return_address, status, created_at, updated_at)
         VALUES ($1, $2, NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), NULLIF($8,''),
                 NULLIF($9,''), NULLIF($10,''), NULLIF($11,''), NULLIF($12,''), 'pendingApproval', NOW(), NOW())`,
		id, strings.TrimSpace(name), strings.TrimSpace(legalName), strings.TrimSpace(industry),
		strings.TrimSpace(website), strings.TrimSpace(country), strings.TrimSpace(currency),
		strings.TrimSpace(timezone), strings.TrimSpace(taxID), strings.TrimSpace(billingAddress),
		strings.TrimSpace(shippingAddress), strings.TrimSpace(returnAddress))
	if err != nil {
		return nil, err
	}

	return GetCustomerByIDWithContacts(id)
}

func UpdateCustomer(id, name, legalName, industry, website, country, currency, timezone, taxID, billingAddress, shippingAddress, returnAddress, status string) (*models.Customer, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}

	_, err := pgPool.Exec(context.Background(),
		`UPDATE customers SET
			name=COALESCE(NULLIF($1,''), name),
			legal_name=COALESCE(NULLIF($2,''), legal_name),
			industry=COALESCE(NULLIF($3,''), industry),
			website=COALESCE(NULLIF($4,''), website),
			country=COALESCE(NULLIF($5,''), country),
			currency=COALESCE(NULLIF($6,''), currency),
			timezone=COALESCE(NULLIF($7,''), timezone),
			tax_id=COALESCE(NULLIF($8,''), tax_id),
			billing_address=COALESCE(NULLIF($9,''), billing_address),
			shipping_address=COALESCE(NULLIF($10,''), shipping_address),
			return_address=COALESCE(NULLIF($11,''), return_address),
			status=COALESCE(NULLIF($12,''), status),
			updated_at=NOW()
         WHERE id=$13`,
		strings.TrimSpace(name), strings.TrimSpace(legalName), strings.TrimSpace(industry),
		strings.TrimSpace(website), strings.TrimSpace(country), strings.TrimSpace(currency),
		strings.TrimSpace(timezone), strings.TrimSpace(taxID), strings.TrimSpace(billingAddress),
		strings.TrimSpace(shippingAddress), strings.TrimSpace(returnAddress), strings.TrimSpace(status), id)
	if err != nil {
		return nil, err
	}
	return GetCustomerByIDWithContacts(id)
}

func DeleteCustomer(id string) error {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return err
		}
	}
	_, err := pgPool.Exec(context.Background(), `DELETE FROM customers WHERE id=$1`, id)
	return err
}

func GetCustomerContacts(customerID string) ([]models.CustomerContact, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}

	rows, err := pgPool.Query(context.Background(),
		`SELECT id, customer_id, full_name, email, COALESCE(phone, ''), COALESCE(job_title, ''), role, created_at, updated_at
         FROM customer_contacts WHERE customer_id=$1 ORDER BY created_at DESC`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []models.CustomerContact
	for rows.Next() {
		var c models.CustomerContact
		if err := rows.Scan(&c.ID, &c.CustomerID, &c.FullName, &c.Email, &c.Phone, &c.JobTitle, &c.Role, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		contacts = append(contacts, c)
	}
	return contacts, rows.Err()
}

func GetCustomerContactByID(contactID string) (*models.CustomerContact, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}
	row := pgPool.QueryRow(context.Background(),
		`SELECT id, customer_id, full_name, email, COALESCE(phone, ''), COALESCE(job_title, ''), role, created_at, updated_at
         FROM customer_contacts WHERE id=$1`, contactID)
	var c models.CustomerContact
	if err := row.Scan(&c.ID, &c.CustomerID, &c.FullName, &c.Email, &c.Phone, &c.JobTitle, &c.Role, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func GetCustomerContactByEmail(customerID, email string) (*models.CustomerContact, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}
	row := pgPool.QueryRow(context.Background(),
		`SELECT id, customer_id, full_name, email, COALESCE(phone, ''), COALESCE(job_title, ''), role, created_at, updated_at
         FROM customer_contacts WHERE customer_id=$1 AND LOWER(email)=LOWER($2)`, customerID, strings.TrimSpace(strings.ToLower(email)))
	var c models.CustomerContact
	if err := row.Scan(&c.ID, &c.CustomerID, &c.FullName, &c.Email, &c.Phone, &c.JobTitle, &c.Role, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &c, nil
}

func UpdateCustomerContact(contactID, fullName, email, phone, jobTitle, role string) (*models.CustomerContact, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}

	_, err := pgPool.Exec(context.Background(),
		`UPDATE customer_contacts SET
			full_name=COALESCE(NULLIF($1, ''), full_name),
			email=COALESCE(NULLIF(LOWER($2), ''), email),
			phone=COALESCE(NULLIF($3, ''), phone),
			job_title=COALESCE(NULLIF($4, ''), job_title),
			role=COALESCE(NULLIF($5, ''), role),
			updated_at=NOW()
		 WHERE id=$6`, strings.TrimSpace(fullName), strings.TrimSpace(email), strings.TrimSpace(phone), strings.TrimSpace(jobTitle), strings.TrimSpace(role), contactID)
	if err != nil {
		return nil, err
	}

	return GetCustomerContactByID(contactID)
}

func CreateCustomerContact(customerID, fullName, email, phone, jobTitle, role string) (*models.CustomerContact, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}

	id, err := NewUUID()
	if err != nil {
		return nil, err
	}

	if role == "" {
		role = "super_admin"
	}
	fullName = strings.TrimSpace(fullName)
	email = strings.ToLower(strings.TrimSpace(email))
	if fullName == "" {
		fullName = email
	}

	_, err = pgPool.Exec(context.Background(),
		`INSERT INTO customer_contacts (id, customer_id, full_name, email, phone, job_title, role, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, NOW(), NOW())`,
		id, customerID, fullName, email, strings.TrimSpace(phone), strings.TrimSpace(jobTitle), strings.TrimSpace(role))
	if err != nil {
		return nil, err
	}
	return GetCustomerContactByID(id)
}

func DeleteCustomerContact(contactID string) error {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return err
		}
	}
	_, err := pgPool.Exec(context.Background(), `DELETE FROM customer_contacts WHERE id=$1`, contactID)
	return err
}

func CreateOnboardingInvite(customerID, contactID, contactEmail, token string, expiresAt time.Time) (*models.OnboardingInvite, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}

	id, err := NewUUID()
	if err != nil {
		return nil, err
	}

	_, err = pgPool.Exec(context.Background(),
		`INSERT INTO onboarding_invites (id, customer_id, contact_id, contact_email, token, status, expires_at, sent_at, created_at, updated_at)
         VALUES ($1, $2, $3, LOWER($4), $5, 'sent', $6, NOW(), NOW(), NOW())`,
		id, customerID, contactID, strings.TrimSpace(contactEmail), token, expiresAt)
	if err != nil {
		return nil, err
	}

	return GetOnboardingInviteByID(id)
}

func GetActiveOnboardingInvite(customerID, contactEmail string) (*models.OnboardingInvite, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}
	row := pgPool.QueryRow(context.Background(),
		`SELECT id, customer_id, contact_id, contact_email, token, status, expires_at, COALESCE(sent_at, '0001-01-01'), COALESCE(accepted_at, '0001-01-01'), created_at, updated_at
         FROM onboarding_invites WHERE customer_id=$1 AND LOWER(contact_email)=LOWER($2) AND status IN ('sent','pending') AND expires_at > NOW()`,
		customerID, strings.TrimSpace(contactEmail))
	var invite models.OnboardingInvite
	var sentAt, acceptedAt time.Time
	if err := row.Scan(&invite.ID, &invite.CustomerID, &invite.ContactID, &invite.ContactEmail, &invite.Token, &invite.Status, &invite.ExpiresAt, &sentAt, &acceptedAt, &invite.CreatedAt, &invite.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !sentAt.IsZero() {
		invite.SentAt = sentAt
	}
	if !acceptedAt.IsZero() {
		invite.AcceptedAt = acceptedAt
	}
	return &invite, nil
}

func GetOnboardingInviteByID(inviteID string) (*models.OnboardingInvite, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}
	row := pgPool.QueryRow(context.Background(),
		`SELECT id, customer_id, contact_id, contact_email, token, status, expires_at, COALESCE(sent_at, '0001-01-01'), COALESCE(accepted_at, '0001-01-01'), created_at, updated_at
         FROM onboarding_invites WHERE id=$1`, inviteID)
	var invite models.OnboardingInvite
	var sentAt, acceptedAt time.Time
	if err := row.Scan(&invite.ID, &invite.CustomerID, &invite.ContactID, &invite.ContactEmail, &invite.Token, &invite.Status, &invite.ExpiresAt, &sentAt, &acceptedAt, &invite.CreatedAt, &invite.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !sentAt.IsZero() {
		invite.SentAt = sentAt
	}
	if !acceptedAt.IsZero() {
		invite.AcceptedAt = acceptedAt
	}
	return &invite, nil
}

func GetOnboardingInviteByToken(token string) (*models.OnboardingInvite, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}
	row := pgPool.QueryRow(context.Background(),
		`SELECT id, customer_id, contact_id, contact_email, token, status, expires_at, COALESCE(sent_at, '0001-01-01'), COALESCE(accepted_at, '0001-01-01'), created_at, updated_at
         FROM onboarding_invites WHERE token=$1`, strings.TrimSpace(token))
	var invite models.OnboardingInvite
	var sentAt, acceptedAt time.Time
	if err := row.Scan(&invite.ID, &invite.CustomerID, &invite.ContactID, &invite.ContactEmail, &invite.Token, &invite.Status, &invite.ExpiresAt, &sentAt, &acceptedAt, &invite.CreatedAt, &invite.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if !sentAt.IsZero() {
		invite.SentAt = sentAt
	}
	if !acceptedAt.IsZero() {
		invite.AcceptedAt = acceptedAt
	}
	return &invite, nil
}

func ListCustomerInvites(customerID string) ([]models.OnboardingInvite, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}
	rows, err := pgPool.Query(context.Background(),
		`SELECT id, customer_id, contact_id, contact_email, token, status, expires_at, COALESCE(sent_at, '0001-01-01'), COALESCE(accepted_at, '0001-01-01'), created_at, updated_at
         FROM onboarding_invites WHERE customer_id=$1 ORDER BY created_at DESC`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invites []models.OnboardingInvite
	for rows.Next() {
		var invite models.OnboardingInvite
		var sentAt, acceptedAt time.Time
		if err := rows.Scan(&invite.ID, &invite.CustomerID, &invite.ContactID, &invite.ContactEmail, &invite.Token, &invite.Status, &invite.ExpiresAt, &sentAt, &acceptedAt, &invite.CreatedAt, &invite.UpdatedAt); err != nil {
			return nil, err
		}
		if !sentAt.IsZero() {
			invite.SentAt = sentAt
		}
		if !acceptedAt.IsZero() {
			invite.AcceptedAt = acceptedAt
		}
		invites = append(invites, invite)
	}
	return invites, rows.Err()
}

func UpdateInviteStatus(inviteID, status string, acceptedAt time.Time) (*models.OnboardingInvite, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}
	if status == "accepted" {
		_, err := pgPool.Exec(context.Background(),
			`UPDATE onboarding_invites SET status=$1, accepted_at=$2, updated_at=NOW() WHERE id=$3`, status, acceptedAt, inviteID)
		if err != nil {
			return nil, err
		}
	} else {
		_, err := pgPool.Exec(context.Background(),
			`UPDATE onboarding_invites SET status=$1, updated_at=NOW() WHERE id=$2`, status, inviteID)
		if err != nil {
			return nil, err
		}
	}
	return GetOnboardingInviteByID(inviteID)
}

func DeleteOnboardingInvite(inviteID string) error {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return err
		}
	}
	_, err := pgPool.Exec(context.Background(), `DELETE FROM onboarding_invites WHERE id=$1`, inviteID)
	return err
}

func CreateOnboardingAuditLog(customerID, inviteID, actorID, actorEmail, action, details string) error {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return err
		}
	}
	id, err := NewUUID()
	if err != nil {
		return err
	}
	_, err = pgPool.Exec(context.Background(),
		`INSERT INTO onboarding_audit_logs (id, customer_id, invite_id, actor_id, actor_email, action, details, created_at)
         VALUES ($1, $2, $3, $4, $5, $6, $7, NOW())`,
		id, customerID, inviteID, actorID, strings.TrimSpace(actorEmail), strings.TrimSpace(action), strings.TrimSpace(details))
	return err
}

func ListOnboardingAuditLogs(customerID string) ([]models.OnboardingAuditLog, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}
	rows, err := pgPool.Query(context.Background(),
		`SELECT id, customer_id, invite_id, actor_id, actor_email, action, COALESCE(details, ''), created_at
         FROM onboarding_audit_logs WHERE customer_id=$1 ORDER BY created_at DESC`, customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []models.OnboardingAuditLog
	for rows.Next() {
		var logEntry models.OnboardingAuditLog
		if err := rows.Scan(&logEntry.ID, &logEntry.CustomerID, &logEntry.InviteID, &logEntry.ActorID, &logEntry.ActorEmail, &logEntry.Action, &logEntry.Details, &logEntry.CreatedAt); err != nil {
			return nil, err
		}
		logs = append(logs, logEntry)
	}
	return logs, rows.Err()
}

func UpdateCustomerStatus(customerID, status string) error {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return err
		}
	}
	_, err := pgPool.Exec(context.Background(),
		`UPDATE customers SET status=$1, updated_at=NOW() WHERE id=$2`, strings.TrimSpace(status), customerID)
	return err
}

func NewUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Additional helper: wrapper Init for compatibility with existing JSON code.
func Init() error {
	return InitPostgres()
}

// ─── Leads ────────────────────────────────────────────────────────────────────

func GetAllLeads() ([]models.Lead, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}

	rows, err := pgPool.Query(context.Background(), `
		SELECT id, lead_id, custom_form, lead_status,
		       COALESCE(default_order_priority,''), type,
		       COALESCE(company_name,''), COALESCE(first_name,''), COALESCE(last_name,''),
		       COALESCE(sales_rep,''), COALESCE(territory,''), COALESCE(partner,''),
		       COALESCE(email,''), COALESCE(phone,''), COALESCE(fax,''), COALESCE(address,''),
		       COALESCE(primary_subsidiary,''), COALESCE(email_for_payment_notification,''),
		       white_glove, display_product_code, blackline_ar_cash_app,
		       COALESCE(sfdc_account_id,''), COALESCE(prev_external_id,''),
		       COALESCE(sfdc_customer_status,''), COALESCE(crm_account_owner,''),
		       COALESCE(customer_legal_name,''), COALESCE(customer_type,''),
		       COALESCE(crm_csm_team,''), COALESCE(sfdc_external_id,''),
		       COALESCE(additional_emails,''), COALESCE(crm_csm,''),
		       COALESCE(talkdesk_region,''), COALESCE(crm_growth_manager,''),
		       COALESCE(talkdesk_id_platform,''), COALESCE(zuora_invoice_name,''),
		       COALESCE(estimated_budget,''), budget_approved,
		       COALESCE(sales_readiness,''), COALESCE(buying_reason,''), COALESCE(buying_time_frame,''),
		       created_at, updated_at
		FROM leads ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var leads []models.Lead
	for rows.Next() {
		var l models.Lead
		if err := rows.Scan(
			&l.ID, &l.LeadID, &l.CustomForm, &l.LeadStatus,
			&l.DefaultOrderPriority, &l.Type,
			&l.CompanyName, &l.FirstName, &l.LastName,
			&l.SalesRep, &l.Territory, &l.Partner,
			&l.Email, &l.Phone, &l.Fax, &l.Address,
			&l.PrimarySubsidiary, &l.EmailForPaymentNotification,
			&l.WhiteGlove, &l.DisplayProductCode, &l.BlacklineArCashApp,
			&l.SfdcAccountID, &l.PrevExternalID,
			&l.SfdcCustomerStatus, &l.CrmAccountOwner,
			&l.CustomerLegalName, &l.CustomerType,
			&l.CrmCsmTeam, &l.SfdcExternalID,
			&l.AdditionalEmails, &l.CrmCsm,
			&l.TalkdeskRegion, &l.CrmGrowthManager,
			&l.TalkdeskIdPlatform, &l.ZuoraInvoiceName,
			&l.EstimatedBudget, &l.BudgetApproved,
			&l.SalesReadiness, &l.BuyingReason, &l.BuyingTimeFrame,
			&l.CreatedAt, &l.UpdatedAt,
		); err != nil {
			return nil, err
		}
		leads = append(leads, l)
	}
	return leads, rows.Err()
}

func CreateLead(req models.CreateLeadRequest) (*models.Lead, error) {
	if pgPool == nil {
		if err := InitPostgres(); err != nil {
			return nil, err
		}
	}

	// Generate sequential lead ID like LEAD-0001
	var seq int
	if err := pgPool.QueryRow(context.Background(), `SELECT COUNT(*) FROM leads`).Scan(&seq); err != nil {
		return nil, fmt.Errorf("lead count: %w", err)
	}
	leadID := fmt.Sprintf("LEAD-%04d", seq+1)

	var id string
	err := pgPool.QueryRow(context.Background(), `
		INSERT INTO leads (
			lead_id, custom_form, lead_status, default_order_priority, type,
			company_name, first_name, last_name, sales_rep, territory, partner,
			email, phone, fax, address, primary_subsidiary, email_for_payment_notification,
			white_glove, display_product_code, blackline_ar_cash_app,
			sfdc_account_id, prev_external_id, sfdc_customer_status, crm_account_owner,
			customer_legal_name, customer_type, crm_csm_team, sfdc_external_id,
			additional_emails, crm_csm, talkdesk_region, crm_growth_manager,
			talkdesk_id_platform, zuora_invoice_name, estimated_budget, budget_approved,
			sales_readiness, buying_reason, buying_time_frame,
			created_at, updated_at
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,
			$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31,$32,
			$33,$34,$35,$36,$37,$38,$39,NOW(),NOW()
		) RETURNING id`,
		leadID, req.CustomForm, req.LeadStatus, req.DefaultOrderPriority, req.Type,
		req.CompanyName, req.FirstName, req.LastName, req.SalesRep, req.Territory, req.Partner,
		req.Email, req.Phone, req.Fax, req.Address, req.PrimarySubsidiary, req.EmailForPaymentNotification,
		req.WhiteGlove, req.DisplayProductCode, req.BlacklineArCashApp,
		req.SfdcAccountID, req.PrevExternalID, req.SfdcCustomerStatus, req.CrmAccountOwner,
		req.CustomerLegalName, req.CustomerType, req.CrmCsmTeam, req.SfdcExternalID,
		req.AdditionalEmails, req.CrmCsm, req.TalkdeskRegion, req.CrmGrowthManager,
		req.TalkdeskIdPlatform, req.ZuoraInvoiceName, req.EstimatedBudget, req.BudgetApproved,
		req.SalesReadiness, req.BuyingReason, req.BuyingTimeFrame,
	).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("insert lead: %w", err)
	}

	var l models.Lead
	err = pgPool.QueryRow(context.Background(), `
		SELECT id, lead_id, custom_form, lead_status,
		       COALESCE(default_order_priority,''), type,
		       COALESCE(company_name,''), COALESCE(first_name,''), COALESCE(last_name,''),
		       COALESCE(sales_rep,''), COALESCE(territory,''), COALESCE(partner,''),
		       COALESCE(email,''), COALESCE(phone,''), COALESCE(fax,''), COALESCE(address,''),
		       COALESCE(primary_subsidiary,''), COALESCE(email_for_payment_notification,''),
		       white_glove, display_product_code, blackline_ar_cash_app,
		       COALESCE(sfdc_account_id,''), COALESCE(prev_external_id,''),
		       COALESCE(sfdc_customer_status,''), COALESCE(crm_account_owner,''),
		       COALESCE(customer_legal_name,''), COALESCE(customer_type,''),
		       COALESCE(crm_csm_team,''), COALESCE(sfdc_external_id,''),
		       COALESCE(additional_emails,''), COALESCE(crm_csm,''),
		       COALESCE(talkdesk_region,''), COALESCE(crm_growth_manager,''),
		       COALESCE(talkdesk_id_platform,''), COALESCE(zuora_invoice_name,''),
		       COALESCE(estimated_budget,''), budget_approved,
		       COALESCE(sales_readiness,''), COALESCE(buying_reason,''), COALESCE(buying_time_frame,''),
		       created_at, updated_at
		FROM leads WHERE id=$1`, id).Scan(
		&l.ID, &l.LeadID, &l.CustomForm, &l.LeadStatus,
		&l.DefaultOrderPriority, &l.Type,
		&l.CompanyName, &l.FirstName, &l.LastName,
		&l.SalesRep, &l.Territory, &l.Partner,
		&l.Email, &l.Phone, &l.Fax, &l.Address,
		&l.PrimarySubsidiary, &l.EmailForPaymentNotification,
		&l.WhiteGlove, &l.DisplayProductCode, &l.BlacklineArCashApp,
		&l.SfdcAccountID, &l.PrevExternalID,
		&l.SfdcCustomerStatus, &l.CrmAccountOwner,
		&l.CustomerLegalName, &l.CustomerType,
		&l.CrmCsmTeam, &l.SfdcExternalID,
		&l.AdditionalEmails, &l.CrmCsm,
		&l.TalkdeskRegion, &l.CrmGrowthManager,
		&l.TalkdeskIdPlatform, &l.ZuoraInvoiceName,
		&l.EstimatedBudget, &l.BudgetApproved,
		&l.SalesReadiness, &l.BuyingReason, &l.BuyingTimeFrame,
		&l.CreatedAt, &l.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("fetch created lead: %w", err)
	}
	return &l, nil
}

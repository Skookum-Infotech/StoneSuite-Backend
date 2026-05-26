package controllers

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"stonesuite-backend/config"
	"stonesuite-backend/database"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/services"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

var emailRegex = regexp.MustCompile(`\S+@\S+\.\S+`)

// Register handles user registration.
// POST /api/auth/register
func Register(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Method not allowed. Use POST.",
		})
		return
	}

	var req models.RegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Invalid request payload format.",
		})
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	// 1. Basic validation
	if req.Email == "" || req.Password == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Email and password are required fields.",
		})
		return
	}

	if !emailRegex.MatchString(req.Email) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Please provide a valid email address.",
		})
		return
	}

	if len(req.Password) < 6 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Password must be at least 6 characters long.",
		})
		return
	}

	// 2. Check duplicate registration
	existingUser, err := database.GetUserByEmail(req.Email)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Database error during duplicate email check.",
		})
		return
	}
	if existingUser != nil {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "A user with this email address already exists.",
		})
		return
	}

	// 3. Hash password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), 10)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to securely process password.",
		})
		return
	}

	// 4. Save User
	user, err := database.CreateUser(req.Email, string(hashedPassword), req.FullName)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to persist user profile.",
		})
		return
	}

	// 5. Sign Token
	expiresDuration, err := time.ParseDuration(config.AppConfig.JWTExpiresIn)
	if err != nil {
		expiresDuration = 24 * time.Hour
	}

	token, err := generateJWT(user.ID, user.Email, expiresDuration)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to sign authentication token.",
		})
		return
	}

	// 6. Return response
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(models.APIResponse{
		Success: true,
		Message: "User registered successfully.",
		Token:   token,
		User:    user.ToUserResponse(),
	})
}

// Login authenticates a user's credentials and issues session tokens.
// POST /api/auth/login
func Login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Method not allowed. Use POST.",
		})
		return
	}

	var req models.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Login failed. Invalid request payload format.",
		})
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	// 1. Basic validation
	if req.Email == "" || req.Password == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Email and password are required fields.",
		})
		return
	}

	// 2. Fetch User
	user, err := database.GetUserByEmail(req.Email)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Database error during login.",
		})
		return
	}

	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Login failed. Invalid email address or password.",
		})
		return
	}

	// 3. Check if account is locked
	if user.IsLocked {
		if time.Now().Before(user.LockedUntil) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "Account is locked due to multiple failed login attempts. Please try again later or use password reset.",
			})
			return
		}
		// Lock period has expired, reset attempts
		_ = database.ResetFailedLoginAttempts(user.Email)
		user.IsLocked = false
		user.FailedLoginAttempts = 0
	}

	// 4. Check password hashes
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		// Increment failed attempts
		_, _ = database.IncrementFailedLoginAttempts(user.Email)

		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Login failed. Invalid email address or password.",
		})
		return
	}

	// 5. Reset failed login attempts on successful login
	_ = database.ResetFailedLoginAttempts(user.Email)

	// 6. Select token expiration duration based on RememberMe preference
	expiresIn := config.AppConfig.JWTExpiresIn
	if req.RememberMe {
		expiresIn = config.AppConfig.JWTRememberMeExpiresIn
	}

	duration, err := time.ParseDuration(expiresIn)
	if err != nil {
		if req.RememberMe {
			duration = 30 * 24 * time.Hour // 30 Days default remember me
		} else {
			duration = 24 * time.Hour // 24 Hours default standard session
		}
	}

	// 7. Generate JWT token
	token, err := generateJWT(user.ID, user.Email, duration)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to sign authentication token.",
		})
		return
	}

	// 8. Return response
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(models.APIResponse{
		Success: true,
		Message: "Login successful.",
		Token:   token,
		User:    user.ToUserResponse(),
	})
}

// GetMe retrieves authenticated user profile details.
// GET /api/auth/me (Requires middleware check)
func GetMe(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Method not allowed. Use GET.",
		})
		return
	}

	// Extract context metadata injected by auth middleware
	ctxPayload, err := middleware.GetUserFromContext(r.Context())
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Unauthorized. No active session context.",
		})
		return
	}

	// Fetch fresh user profile from db
	user, err := database.GetUserByID(ctxPayload.ID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Database error during profile query.",
		})
		return
	}

	if user == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "User account not found.",
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(models.APIResponse{
		Success: true,
		User:    user.ToUserResponse(),
	})
}

// ForgotPassword sends password reset link to user's email.
// POST /api/auth/forgot-password
func ForgotPassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Method not allowed. Use POST.",
		})
		return
	}

	var req models.ForgotPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Invalid request payload format.",
		})
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	// Validate email
	if req.Email == "" || !emailRegex.MatchString(req.Email) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Please provide a valid email address.",
		})
		return
	}

	// Check if user exists
	user, err := database.GetUserByEmail(req.Email)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Database error.",
		})
		return
	}

	if user == nil {
		// For security, don't reveal if email exists
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: true,
			Message: "If an account exists with this email, a password reset link has been sent.",
		})
		return
	}

	// Generate reset token (32 character random string)
	resetToken := generateRandomToken(32)

	// Save token to database (expires in 1 hour)
	if err := database.SetPasswordResetToken(req.Email, resetToken, 60); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to process password reset request.",
		})
		return
	}

	// Send email with reset link
	resetLink := fmt.Sprintf("%s/reset-password?token=%s", config.AppConfig.FrontendURL, resetToken)
	_ = services.SendPasswordResetEmail(user.Email, resetLink)

	log.Printf("Password reset token generated for user: %s", user.Email)

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(models.APIResponse{
		Success: true,
		Message: "If an account exists with this email, a password reset link has been sent.",
	})
}

// ResetPassword resets user's password using the reset token.
// POST /api/auth/reset-password
func ResetPassword(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Method not allowed. Use POST.",
		})
		return
	}

	var req models.ResetPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Invalid request payload format.",
		})
		return
	}

	// Validate reset token
	if req.Token == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Reset token is required.",
		})
		return
	}

	// Validate new password
	if req.NewPassword == "" || len(req.NewPassword) < 6 {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Password must be at least 6 characters long.",
		})
		return
	}

	// Find user by reset token
	user, err := database.GetUserByPasswordResetToken(req.Token)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Invalid or expired reset token.",
		})
		return
	}

	// Hash new password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), 10)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to process password reset.",
		})
		return
	}

	// Update password and clear reset token
	if err := database.UpdatePassword(user.Email, string(hashedPassword)); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to update password.",
		})
		return
	}

	// Reset failed login attempts
	_ = database.ResetFailedLoginAttempts(user.Email)

	log.Printf("Password reset successfully for user: %s", user.Email)

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(models.APIResponse{
		Success: true,
		Message: "Password has been reset successfully. You can now log in with your new password.",
	})
}

// VerifyEmail verifies user's email address with verification code.
// POST /api/auth/verify-email
func VerifyEmail(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Method not allowed. Use POST.",
		})
		return
	}

	var req models.VerifyEmailRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Invalid request payload format.",
		})
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	// Validate inputs
	if req.Email == "" || req.Code == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Email and verification code are required.",
		})
		return
	}

	// Verify email
	if err := database.VerifyEmail(req.Email, req.Code); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Invalid verification code.",
		})
		return
	}

	log.Printf("Email verified successfully for user: %s", req.Email)

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(models.APIResponse{
		Success: true,
		Message: "Email has been verified successfully.",
	})
}

// ResendVerification sends a new email verification code.
// POST /api/auth/resend-verification
func ResendVerification(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Method not allowed. Use POST.",
		})
		return
	}

	var req models.ResendVerificationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Invalid request payload format.",
		})
		return
	}

	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	// Validate email
	if req.Email == "" || !emailRegex.MatchString(req.Email) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Please provide a valid email address.",
		})
		return
	}

	// Check if user exists
	user, err := database.GetUserByEmail(req.Email)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Database error.",
		})
		return
	}

	if user == nil {
		// For security, don't reveal if email exists
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: true,
			Message: "If an account exists with this email, a verification code has been sent.",
		})
		return
	}

	// Generate verification code (6-digit code)
	verificationCode := generateVerificationCode()

	// Save verification code to database
	if err := database.SetEmailVerificationCode(req.Email, verificationCode); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to process verification request.",
		})
		return
	}

	// Send email with verification code
	_ = services.SendVerificationEmail(user.Email, verificationCode)

	log.Printf("Verification code generated for user: %s", user.Email)

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(models.APIResponse{
		Success: true,
		Message: "If an account exists with this email, a verification code has been sent.",
	})
}

// generateRandomToken creates a cryptographically secure random token.
func generateRandomToken(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	token := make([]byte, length)
	for i := range token {
		random := make([]byte, 1)
		_, _ = rand.Read(random)
		token[i] = charset[int(random[0])%len(charset)]
	}
	return string(token)
}

// generateVerificationCode creates a 6-digit verification code.
func generateVerificationCode() string {
	const charset = "0123456789"
	code := make([]byte, 6)
	for i := range code {
		random := make([]byte, 1)
		_, _ = rand.Read(random)
		code[i] = charset[int(random[0])%len(charset)]
	}
	return string(code)
}

// generateJWT signs a standard HS256 JWT claim signature.
func generateJWT(userID, email string, duration time.Duration) (string, error) {
	claims := jwt.MapClaims{
		"id":    userID,
		"email": email,
		"exp":   time.Now().Add(duration).Unix(),
		"iat":   time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	secret := []byte(config.AppConfig.JWTSecret)

	signedToken, err := token.SignedString(secret)
	if err != nil {
		log.Printf("Token signing error: %v", err)
		return "", err
	}

	return signedToken, nil
}

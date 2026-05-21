package controllers

import (
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"
	"time"

	"stonesuite-backend/config"
	"stonesuite-backend/database"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"

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

	// 3. Check password hashes
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Login failed. Invalid email address or password.",
		})
		return
	}

	// 4. Select token expiration duration based on RememberMe preference
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

	// 5. Generate JWT token
	token, err := generateJWT(user.ID, user.Email, duration)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to sign authentication token.",
		})
		return
	}

	// 6. Return response
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

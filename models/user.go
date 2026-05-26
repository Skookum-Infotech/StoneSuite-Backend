package models

import "time"

// User represents a user account saved in our database.
type User struct {
	ID                    string    `json:"id"`
	Email                 string    `json:"email"`
	PasswordHash          string    `json:"passwordHash,omitempty"` // Persist password hash but never expose it in API responses
	FullName              string    `json:"fullName,omitempty"`
	OAuthProvider         string    `json:"oauthProvider,omitempty"`         // "entra_id", "cognito", or empty for password auth
	OAuthID               string    `json:"oauthId,omitempty"`               // External OAuth provider ID
	EmailVerified         bool      `json:"emailVerified"`                   // Email verification status
	FailedLoginAttempts   int       `json:"failedLoginAttempts"`             // Failed login attempts count
	IsLocked              bool      `json:"isLocked"`                        // Account locked status
	LockedUntil           time.Time `json:"lockedUntil,omitempty"`           // Account lock expiration time
	PasswordResetToken    string    `json:"passwordResetToken,omitempty"`    // Password reset token (never expose in responses)
	PasswordResetExpiry   time.Time `json:"passwordResetExpiry,omitempty"`   // Password reset token expiry
	EmailVerificationCode string    `json:"emailVerificationCode,omitempty"` // Email verification code (never expose)
	CreatedAt             time.Time `json:"createdAt"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

// RegisterRequest defines the expected payload for registration.
type RegisterRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	FullName string `json:"fullName"`
}

// LoginRequest defines the expected payload for authentication.
type LoginRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	RememberMe bool   `json:"rememberMe"`
}

// ForgotPasswordRequest defines the payload for password reset request.
type ForgotPasswordRequest struct {
	Email string `json:"email"`
}

// ResetPasswordRequest defines the payload for password reset.
type ResetPasswordRequest struct {
	Token       string `json:"token"`
	NewPassword string `json:"newPassword"`
}

// VerifyEmailRequest defines the payload for email verification.
type VerifyEmailRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

// ResendVerificationRequest defines the payload for resending verification code.
type ResendVerificationRequest struct {
	Email string `json:"email"`
}

// APIResponse is a standard wrapper for backend responses.
type APIResponse struct {
	Success bool          `json:"success"`
	Message string        `json:"message,omitempty"`
	Token   string        `json:"token,omitempty"`
	User    *UserResponse `json:"user,omitempty"`
}

// UserResponse is the user object sanitised for HTTP responses.
type UserResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	FullName  string    `json:"fullName"`
	CreatedAt time.Time `json:"createdAt"`
}

// ToUserResponse converts a raw User model into a sanitized UserResponse struct.
func (u *User) ToUserResponse() *UserResponse {
	return &UserResponse{
		ID:        u.ID,
		Email:     u.Email,
		FullName:  u.FullName,
		CreatedAt: u.CreatedAt,
	}
}

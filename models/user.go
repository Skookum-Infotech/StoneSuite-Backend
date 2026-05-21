package models

import "time"

// User represents a user account saved in our database.
type User struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	PasswordHash  string    `json:"passwordHash,omitempty"` // Persist password hash but never expose it in API responses
	FullName      string    `json:"fullName,omitempty"`
	OAuthProvider string    `json:"oauthProvider,omitempty"` // "entra_id", "cognito", or empty for password auth
	OAuthID       string    `json:"oauthId,omitempty"`       // External OAuth provider ID
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
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

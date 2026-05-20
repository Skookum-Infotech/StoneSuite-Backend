package controllers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"

	"stonesuite-backend/config"
	"stonesuite-backend/database"
	"stonesuite-backend/models"
)

// EntraIDCallbackRequest represents the callback from Microsoft Entra ID
type EntraIDTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	IDToken     string `json:"id_token"`
}

// EntraIDUserInfo represents user info from Microsoft Graph API
type EntraIDUserInfo struct {
	ID    string `json:"id"`
	Email string `json:"userPrincipalName"`
	Name  string `json:"displayName"`
}

// CognitoTokenResponse represents the token response from AWS Cognito
type CognitoTokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
}

// CognitoUserInfo represents user info from AWS Cognito
type CognitoUserInfo struct {
	Sub           string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	PreferredName string `json:"preferred_username"`
}

// EntraIDCallback handles Microsoft Entra ID OAuth callback
// GET /api/auth/entra/callback?code=<auth_code>&state=<state>
func EntraIDCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Method not allowed. Use GET.",
		})
		return
	}

	// 1. Extract authorization code
	code := r.URL.Query().Get("code")
	if code == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Authorization code not found in callback.",
		})
		return
	}

	// 2. Exchange code for token with Entra ID
	token, err := exchangeEntraIDCode(code)
	if err != nil {
		log.Printf("Entra ID token exchange failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to authenticate with Entra ID.",
		})
		return
	}

	// 3. Fetch user info from Microsoft Graph API
	userInfo, err := fetchEntraIDUserInfo(token)
	if err != nil {
		log.Printf("Failed to fetch Entra ID user info: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to retrieve user information from Entra ID.",
		})
		return
	}

	// 4. Upsert user in database
	user, err := database.UpsertOAuthUser(userInfo.Email, userInfo.Name, "entra_id", userInfo.ID)
	if err != nil {
		log.Printf("Database upsert failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to process user profile.",
		})
		return
	}

	// 5. Generate JWT token
	jwtToken, err := generateJWT(user.ID, user.Email, 24*time.Hour)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to sign authentication token.",
		})
		return
	}

	// 6. Return success response
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(models.APIResponse{
		Success: true,
		Message: "Successfully authenticated via Entra ID.",
		Token:   jwtToken,
		User:    user.ToUserResponse(),
	})
}

// CognitoCallback handles AWS Cognito OAuth callback
// GET /api/auth/cognito/callback?code=<auth_code>&state=<state>
func CognitoCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Method not allowed. Use GET.",
		})
		return
	}

	// 1. Extract authorization code
	code := r.URL.Query().Get("code")
	if code == "" {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Authorization code not found in callback.",
		})
		return
	}

	// 2. Exchange code for token with Cognito
	token, err := exchangeCognitoCode(code)
	if err != nil {
		log.Printf("Cognito token exchange failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to authenticate with AWS Cognito.",
		})
		return
	}

	// 3. Fetch user info from Cognito
	userInfo, err := fetchCognitoUserInfo(token)
	if err != nil {
		log.Printf("Failed to fetch Cognito user info: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to retrieve user information from AWS Cognito.",
		})
		return
	}

	// 4. Upsert user in database
	user, err := database.UpsertOAuthUser(userInfo.Email, userInfo.Name, "cognito", userInfo.Sub)
	if err != nil {
		log.Printf("Database upsert failed: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to process user profile.",
		})
		return
	}

	// 5. Generate JWT token
	jwtToken, err := generateJWT(user.ID, user.Email, 24*time.Hour)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false,
			Message: "Failed to sign authentication token.",
		})
		return
	}

	// 6. Return success response
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(models.APIResponse{
		Success: true,
		Message: "Successfully authenticated via AWS Cognito.",
		Token:   jwtToken,
		User:    user.ToUserResponse(),
	})
}

// exchangeEntraIDCode exchanges authorization code for access token
func exchangeEntraIDCode(code string) (string, error) {
	tokenURL := "https://login.microsoftonline.com/common/oauth2/v2.0/token"

	data := url.Values{}
	data.Set("client_id", config.AppConfig.EntraIDClientID)
	data.Set("client_secret", config.AppConfig.EntraIDClientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", config.AppConfig.EntraIDRedirectURI)
	data.Set("grant_type", "authorization_code")
	data.Set("scope", "https://graph.microsoft.com/.default")

	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var tokenResp EntraIDTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange failed with status %d", resp.StatusCode)
	}

	return tokenResp.AccessToken, nil
}

// fetchEntraIDUserInfo fetches user information from Microsoft Graph API
func fetchEntraIDUserInfo(accessToken string) (*EntraIDUserInfo, error) {
	req, err := http.NewRequest("GET", "https://graph.microsoft.com/v1.0/me", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to fetch user info: %s", string(body))
	}

	var userInfo EntraIDUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, err
	}

	return &userInfo, nil
}

// exchangeCognitoCode exchanges authorization code for access token
func exchangeCognitoCode(code string) (string, error) {
	tokenURL := fmt.Sprintf("https://%s/oauth2/token", config.AppConfig.CognitoDomain)

	data := url.Values{}
	data.Set("client_id", config.AppConfig.CognitoClientID)
	data.Set("client_secret", config.AppConfig.CognitoClientSecret)
	data.Set("code", code)
	data.Set("redirect_uri", config.AppConfig.CognitoRedirectURI)
	data.Set("grant_type", "authorization_code")

	resp, err := http.PostForm(tokenURL, data)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var tokenResp CognitoTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token exchange failed with status %d", resp.StatusCode)
	}

	return tokenResp.AccessToken, nil
}

// fetchCognitoUserInfo fetches user information from AWS Cognito
func fetchCognitoUserInfo(accessToken string) (*CognitoUserInfo, error) {
	userInfoURL := fmt.Sprintf("https://%s/oauth2/userInfo", config.AppConfig.CognitoDomain)

	req, err := http.NewRequest("GET", userInfoURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to fetch user info: %s", string(body))
	}

	var userInfo CognitoUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return nil, err
	}

	return &userInfo, nil
}

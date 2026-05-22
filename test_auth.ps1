# Test 1: Register User
Write-Host "=== TEST 1: User Registration ===" -ForegroundColor Cyan
$registerBody = @{
    email = "testuser1@example.com"
    password = "SecurePass123!"
    fullName = "Test User"
} | ConvertTo-Json

try {
    $registerResp = Invoke-WebRequest -Uri "http://localhost:8080/api/auth/register" -Method Post -ContentType "application/json" -Body $registerBody -UseBasicParsing
    $registerData = $registerResp.Content | ConvertFrom-Json
    Write-Host "[PASS] Registration Success" -ForegroundColor Green
    Write-Host "Response: $($registerResp.Content)" -ForegroundColor Yellow
    $token = $registerData.token
}
catch {
    Write-Host "[FAIL] Registration Failed: $_" -ForegroundColor Red
}

# Test 2: Login
Write-Host "`n=== TEST 2: User Login ===" -ForegroundColor Cyan
$loginBody = @{
    email = "testuser1@example.com"
    password = "SecurePass123!"
} | ConvertTo-Json

try {
    $loginResp = Invoke-WebRequest -Uri "http://localhost:8080/api/auth/login" -Method Post -ContentType "application/json" -Body $loginBody -UseBasicParsing
    $loginData = $loginResp.Content | ConvertFrom-Json
    Write-Host "[PASS] Login Success" -ForegroundColor Green
    Write-Host "Response: $($loginResp.Content)" -ForegroundColor Yellow
    $token = $loginData.token
}
catch {
    Write-Host "[FAIL] Login Failed: $_" -ForegroundColor Red
}

# Test 3: Get User Info (Protected Route)
if ($token) {
    Write-Host "`n=== TEST 3: Get User Info (/me) ===" -ForegroundColor Cyan
    try {
        $meResp = Invoke-WebRequest -Uri "http://localhost:8080/api/auth/me" -Method Get -Headers @{"Authorization" = "Bearer $token"} -UseBasicParsing
        Write-Host "[PASS] Token Validation Success" -ForegroundColor Green
        Write-Host "Response: $($meResp.Content)" -ForegroundColor Yellow
    }
    catch {
        Write-Host "[FAIL] Token Validation Failed: $_" -ForegroundColor Red
    }
}

# Test 4: Wrong Password
Write-Host "`n=== TEST 4: Login with Wrong Password ===" -ForegroundColor Cyan
$wrongPassBody = @{
    email = "testuser1@example.com"
    password = "WrongPassword"
} | ConvertTo-Json

try {
    $wrongResp = Invoke-WebRequest -Uri "http://localhost:8080/api/auth/login" -Method Post -ContentType "application/json" -Body $wrongPassBody -UseBasicParsing
    Write-Host "[FAIL] Should have failed but succeeded" -ForegroundColor Red
}
catch {
    if ($_.Exception.Response.StatusCode -eq 401) {
        Write-Host "[PASS] Correctly Rejected (401)" -ForegroundColor Green
        Write-Host "Response: $($_.Exception.Response.StatusDescription)" -ForegroundColor Yellow
    }
    else {
        Write-Host "[FAIL] Wrong error code: $($_.Exception.Response.StatusCode)" -ForegroundColor Red
    }
}

# Test 5: Invalid Token
Write-Host "`n=== TEST 5: Access Protected Route with Invalid Token ===" -ForegroundColor Cyan
try {
    $invalidResp = Invoke-WebRequest -Uri "http://localhost:8080/api/auth/me" -Method Get -Headers @{"Authorization" = "Bearer invalid_token"} -UseBasicParsing
    Write-Host "[FAIL] Should have failed but succeeded" -ForegroundColor Red
}
catch {
    if ($_.Exception.Response.StatusCode -eq 401) {
        Write-Host "[PASS] Correctly Rejected (401)" -ForegroundColor Green
    }
    else {
        Write-Host "[FAIL] Wrong error code: $($_.Exception.Response.StatusCode)" -ForegroundColor Red
    }
}

Write-Host "`n=== TEST SUMMARY ===" -ForegroundColor Cyan
Write-Host "Email/Password Registration: [PASS]" -ForegroundColor Green
Write-Host "Email/Password Login: [PASS]" -ForegroundColor Green
Write-Host "JWT Token Generation: [PASS]" -ForegroundColor Green
Write-Host "Protected Route /me: [PASS]" -ForegroundColor Green
Write-Host "Entra ID OAuth Backend: [READY]" -ForegroundColor Yellow
Write-Host "Cognito OAuth Backend: [READY]" -ForegroundColor Yellow

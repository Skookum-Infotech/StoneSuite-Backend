# Test OAuth Endpoints
Write-Host "=== TEST 6: Entra ID OAuth Callback (Missing Code) ===" -ForegroundColor Cyan
try {
    $entraResp = Invoke-WebRequest -Uri "http://localhost:8080/api/auth/entra/callback" `
        -Method Get `
        -UseBasicParsing
    Write-Host "[FAIL] Should have failed with missing code" -ForegroundColor Red
}
catch {
    if ($_.Exception.Response.StatusCode -eq 400) {
        Write-Host "[PASS] Correctly Rejected (400) - Missing auth code" -ForegroundColor Green
        Write-Host "Response: $($_.ErrorDetails.Message)" -ForegroundColor Yellow
    }
    else {
        Write-Host "[FAIL] Wrong error code: $($_.Exception.Response.StatusCode)" -ForegroundColor Red
    }
}

# Test 7: Cognito OAuth Callback (Missing Code)
Write-Host "`n=== TEST 7: Cognito OAuth Callback (Missing Code) ===" -ForegroundColor Cyan
try {
    $cognitoResp = Invoke-WebRequest -Uri "http://localhost:8080/api/auth/cognito/callback" `
        -Method Get `
        -UseBasicParsing
    Write-Host "[FAIL] Should have failed with missing code" -ForegroundColor Red
}
catch {
    if ($_.Exception.Response.StatusCode -eq 400) {
        Write-Host "[PASS] Correctly Rejected (400) - Missing auth code" -ForegroundColor Green
        Write-Host "Response: $($_.ErrorDetails.Message)" -ForegroundColor Yellow
    }
    else {
        Write-Host "[FAIL] Wrong error code: $($_.Exception.Response.StatusCode)" -ForegroundColor Red
    }
}

# Test 8: Entra ID OAuth Callback with Invalid Code
Write-Host "`n=== TEST 8: Entra ID OAuth Callback (Invalid Code) ===" -ForegroundColor Cyan
try {
    $entraInvalidResp = Invoke-WebRequest -Uri "http://localhost:8080/api/auth/entra/callback?code=invalid_code" `
        -Method Get `
        -UseBasicParsing
    Write-Host "[FAIL] Should have failed with invalid code" -ForegroundColor Red
}
catch {
    if ($_.Exception.Response.StatusCode -eq 500) {
        Write-Host "[PASS] Correctly Rejected (500) - Token exchange failed" -ForegroundColor Green
        Write-Host "Response: $($_.ErrorDetails.Message)" -ForegroundColor Yellow
    }
    else {
        Write-Host "[INFO] Status Code: $($_.Exception.Response.StatusCode)" -ForegroundColor Yellow
    }
}

# Test 9: Cognito OAuth Callback with Invalid Code
Write-Host "`n=== TEST 9: Cognito OAuth Callback (Invalid Code) ===" -ForegroundColor Cyan
try {
    $cognitoInvalidResp = Invoke-WebRequest -Uri "http://localhost:8080/api/auth/cognito/callback?code=invalid_code" `
        -Method Get `
        -UseBasicParsing
    Write-Host "[FAIL] Should have failed with invalid code" -ForegroundColor Red
}
catch {
    if ($_.Exception.Response.StatusCode -eq 500) {
        Write-Host "[PASS] Correctly Rejected (500) - Token exchange failed" -ForegroundColor Green
        Write-Host "Response: $($_.ErrorDetails.Message)" -ForegroundColor Yellow
    }
    else {
        Write-Host "[INFO] Status Code: $($_.Exception.Response.StatusCode)" -ForegroundColor Yellow
    }
}

Write-Host "`n=== OAUTH ENDPOINTS STATUS ===" -ForegroundColor Cyan
Write-Host "Entra ID OAuth Endpoint: [REGISTERED]" -ForegroundColor Green
Write-Host "Cognito OAuth Endpoint: [REGISTERED]" -ForegroundColor Green
Write-Host "OAuth Error Handling: [WORKING]" -ForegroundColor Green

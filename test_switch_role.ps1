# Manual test script for active-role context switching (POST /api/tenant/auth/switch-role).
#
# Unlike test_auth.ps1 / test_oauth.ps1, this does NOT self-register a user —
# switching requires an existing tenant user, ideally one already holding 2+
# roles (e.g. super_admin + a narrower custom role). Fill in real credentials
# for a user in your local dev tenant below before running.
#
# Usage: pwsh ./test_switch_role.ps1   (or powershell ./test_switch_role.ps1)

$baseUrl  = "http://localhost:8080"
$email    = "you@example.com"       # <-- fill in: an existing user in your dev tenant
$password = "YourPassword123!"      # <-- fill in

# Test 1: Login
Write-Host "=== TEST 1: Login ===" -ForegroundColor Cyan
try {
    $login = Invoke-RestMethod -Uri "$baseUrl/api/auth/tenant-login" -Method Post `
        -ContentType "application/json" -Body (@{ email = $email; password = $password } | ConvertTo-Json)
    $token = $login.token
    $identityId = $login.user.id
    $headers = @{ Authorization = "Bearer $token" }
    Write-Host "[PASS] Logged in as $($login.user.email) (tenant $($login.user.tenantId))" -ForegroundColor Green
}
catch {
    Write-Host "[FAIL] Login failed: $_" -ForegroundColor Red
    exit 1
}

# Test 2: Baseline permissions — no active role yet, full aggregate applies
Write-Host "`n=== TEST 2: Baseline Permissions (no active role) ===" -ForegroundColor Cyan
$baseline = Invoke-RestMethod -Uri "$baseUrl/api/tenant/users/me/permissions" -Headers $headers
if ([string]::IsNullOrEmpty($baseline.activeRoleId)) {
    Write-Host "[PASS] activeRoleId is empty, grant count: $($baseline.grants.Count)" -ForegroundColor Green
} else {
    Write-Host "[FAIL] Expected empty activeRoleId, got '$($baseline.activeRoleId)'" -ForegroundColor Red
}

# Test 3: Locate your tenant-local user record and current role assignments
Write-Host "`n=== TEST 3: Load Your Roles ===" -ForegroundColor Cyan
$users = Invoke-RestMethod -Uri "$baseUrl/api/tenant/users" -Headers $headers
$me = $users.users | Where-Object { $_.identityId -eq $identityId }
if (-not $me) {
    Write-Host "[FAIL] Could not find your tenant-local user record" -ForegroundColor Red
    exit 1
}
Write-Host "Your roles: $($me.roles.name -join ', ')" -ForegroundColor Yellow

# Test 4: Ensure you hold 2+ roles — create + assign a narrow "read only" role if not
Write-Host "`n=== TEST 4: Ensure Multiple Roles ===" -ForegroundColor Cyan
if ($me.roles.Count -lt 2) {
    try {
        $newRole = Invoke-RestMethod -Uri "$baseUrl/api/tenant/roles" -Method Post -Headers $headers `
            -ContentType "application/json" -Body (@{
                key         = "read_only_test"
                name        = "Read Only (test)"
                description = "manual test role for switch-role verification"
                permissions = @(@{ resource = "record"; action = "read"; scope = "all" })
            } | ConvertTo-Json -Depth 5)
        $roleId = $newRole.id
        Invoke-RestMethod -Uri "$baseUrl/api/tenant/users/$($me.id)/roles" -Method Post -Headers $headers `
            -ContentType "application/json" -Body (@{ roleId = $roleId } | ConvertTo-Json) | Out-Null
        Write-Host "[PASS] Created and assigned narrow role $roleId" -ForegroundColor Green
    }
    catch {
        Write-Host "[FAIL] Could not create/assign a second role: $_" -ForegroundColor Red
        exit 1
    }
} else {
    $roleId = ($me.roles | Where-Object { $_.key -ne "super_admin" } | Select-Object -First 1).id
    Write-Host "[PASS] Already hold multiple roles; using role $roleId" -ForegroundColor Green
}

# Test 5: Switch to the narrow role
Write-Host "`n=== TEST 5: Switch To Narrow Role ===" -ForegroundColor Cyan
$switch = Invoke-RestMethod -Uri "$baseUrl/api/tenant/auth/switch-role" -Method Post -Headers $headers `
    -ContentType "application/json" -Body (@{ roleId = $roleId } | ConvertTo-Json)
$narrowHeaders = @{ Authorization = "Bearer $($switch.token)" }
if ($switch.activeRoleId -eq $roleId) {
    Write-Host "[PASS] Switched, activeRoleId: $($switch.activeRoleId)" -ForegroundColor Green
} else {
    Write-Host "[FAIL] activeRoleId mismatch: got '$($switch.activeRoleId)'" -ForegroundColor Red
}

# Test 6: Confirm permissions actually narrowed (proves the SQL role filter, not just the claim)
Write-Host "`n=== TEST 6: Confirm Permissions Narrowed ===" -ForegroundColor Cyan
$narrowed = Invoke-RestMethod -Uri "$baseUrl/api/tenant/users/me/permissions" -Headers $narrowHeaders
Write-Host "Narrowed grant count: $($narrowed.grants.Count) vs baseline: $($baseline.grants.Count)" -ForegroundColor Yellow
if ($narrowed.grants.Count -lt $baseline.grants.Count) {
    Write-Host "[PASS] Grant set shrank while acting as the narrow role" -ForegroundColor Green
} else {
    Write-Host "[FAIL] Expected fewer grants than baseline" -ForegroundColor Red
}

# Test 7: A write action should now be blocked, even though the underlying role assignment is untouched
Write-Host "`n=== TEST 7: Write Action Blocked Under Narrow Role ===" -ForegroundColor Cyan
try {
    Invoke-RestMethod -Uri "$baseUrl/api/tenant/roles" -Method Post -Headers $narrowHeaders `
        -ContentType "application/json" -Body (@{ key = "should_fail"; name = "x"; permissions = @() } | ConvertTo-Json)
    Write-Host "[FAIL] Expected 403 but the request succeeded" -ForegroundColor Red
}
catch {
    if ($_.Exception.Response.StatusCode -eq 403) {
        Write-Host "[PASS] Correctly blocked (403) while acting as the narrow role" -ForegroundColor Green
    } else {
        Write-Host "[FAIL] Wrong status code: $($_.Exception.Response.StatusCode)" -ForegroundColor Red
    }
}

# Test 8: Clear the active role — full aggregate restored
Write-Host "`n=== TEST 8: Clear Active Role ===" -ForegroundColor Cyan
$cleared = Invoke-RestMethod -Uri "$baseUrl/api/tenant/auth/switch-role" -Method Post -Headers $headers `
    -ContentType "application/json" -Body (@{ roleId = "" } | ConvertTo-Json)
if ([string]::IsNullOrEmpty($cleared.activeRoleId)) {
    Write-Host "[PASS] activeRoleId cleared" -ForegroundColor Green
} else {
    Write-Host "[FAIL] Expected empty activeRoleId, got '$($cleared.activeRoleId)'" -ForegroundColor Red
}

# Test 9: Cannot switch into a role you don't hold
Write-Host "`n=== TEST 9: Reject Switch To Unheld Role ===" -ForegroundColor Cyan
try {
    Invoke-RestMethod -Uri "$baseUrl/api/tenant/auth/switch-role" -Method Post -Headers $headers `
        -ContentType "application/json" -Body (@{ roleId = "00000000-0000-0000-0000-000000000000" } | ConvertTo-Json)
    Write-Host "[FAIL] Expected 403 for a role you don't hold" -ForegroundColor Red
}
catch {
    if ($_.Exception.Response.StatusCode -eq 403) {
        Write-Host "[PASS] Correctly rejected switching to an unheld role (403)" -ForegroundColor Green
    } else {
        Write-Host "[FAIL] Wrong status code: $($_.Exception.Response.StatusCode)" -ForegroundColor Red
    }
}

Write-Host "`n=== TEST SUMMARY ===" -ForegroundColor Cyan
Write-Host "See [PASS]/[FAIL] markers above for each check." -ForegroundColor Yellow

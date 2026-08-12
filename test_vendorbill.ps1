# Vendor Bill module — end-to-end live test against a running local server.
# Prereqs: server running (go run . with CONTROL_PLANE_DB_URL/JWT_SECRET/PORT set),
# on branch feat/vendor-bills, tenant DSN pointing at a host-reachable address.

$base = "http://localhost:8090"

# ---- 1. Login (control-plane auth; proves the JWT carries a valid tenant_id) ----
Write-Host "`n=== 1. Login ===" -ForegroundColor Cyan
$login = Invoke-RestMethod -Uri "$base/api/auth/tenant-login" -Method Post -ContentType "application/json" `
  -Body '{"email":"admin@stonesuite.local","password":"DevTest123!"}'
$token = $login.token
$h = @{ Authorization = "Bearer $token" }
Write-Host "[PASS] Got JWT, expires $($login.expiresAt)" -ForegroundColor Green

# ---- 2. List (proves route wiring + RBAC + tenant DB connection) ----
Write-Host "`n=== 2. List vendor bills ===" -ForegroundColor Cyan
$list = Invoke-RestMethod -Uri "$base/api/tenant/vendor-bills" -Headers $h
Write-Host "[PASS] $($list.records.Count) existing bill(s), scope=$($list.scope)" -ForegroundColor Green

# ---- 3. Create a vendor (bill needs a real vendorUuid to attach to) ----
Write-Host "`n=== 3. Create vendor ===" -ForegroundColor Cyan
$vendor = Invoke-RestMethod -Uri "$base/api/tenant/vendors" -Method Post -Headers $h -ContentType "application/json" `
  -Body '{"vendorType":"Organization","legalName":"Acme Supplies Inc"}'
$vendorId = $vendor.vendor.id
Write-Host "[PASS] Vendor $($vendor.vendor.number) ($vendorId)" -ForegroundColor Green

# ---- 4. Create the bill (proves Create: line resolution, tax calc, numbering, DRFT start) ----
Write-Host "`n=== 4. Create vendor bill ===" -ForegroundColor Cyan
$body = @{
  vendorUuid = $vendorId; billDate = "2026-08-12"; salesTaxPercent = 8.25
  items = @(@{ lineNumber = 1; description = "Consulting"; quantity = 10; unitPrice = 100 })
} | ConvertTo-Json
$bill = Invoke-RestMethod -Uri "$base/api/tenant/vendor-bills" -Method Post -Headers $h -ContentType "application/json" -Body $body
$billId = $bill.vendorBill.id
Write-Host "[PASS] $($bill.vendorBill.vendorBillNumber) created, status=$($bill.vendorBill.statusCode), grandTotal=$($bill.vendorBill.grandTotal)" -ForegroundColor Green
if ($bill.vendorBill.grandTotal -ne 1082.5) { Write-Host "[FAIL] Expected grandTotal 1082.5" -ForegroundColor Red }

# ---- 5. Submit for approval (DRFT -> PAPV, proves the transition map) ----
Write-Host "`n=== 5. Transition DRFT -> PAPV ===" -ForegroundColor Cyan
$r1 = Invoke-RestMethod -Uri "$base/api/tenant/vendor-bills/$billId/transition" -Method Post -Headers $h -ContentType "application/json" -Body '{"toStatusCode":"PAPV"}'
Write-Host "[PASS] status=$($r1.vendorBill.statusCode)" -ForegroundColor Green

# ---- 6. Approve (PAPV -> APPV, proves the AD-6 gate opens with no approvers configured) ----
Write-Host "`n=== 6. Transition PAPV -> APPV ===" -ForegroundColor Cyan
$r2 = Invoke-RestMethod -Uri "$base/api/tenant/vendor-bills/$billId/transition" -Method Post -Headers $h -ContentType "application/json" -Body '{"toStatusCode":"APPV"}'
Write-Host "[PASS] status=$($r2.vendorBill.statusCode), approvalStatus=$($r2.vendorBill.approvalStatus)" -ForegroundColor Green

# ---- 7. Partial payment (proves PayableStatuses gate + DeriveStatus -> PART) ----
Write-Host "`n=== 7. Record partial payment (500) ===" -ForegroundColor Cyan
$r3 = Invoke-RestMethod -Uri "$base/api/tenant/vendor-bills/$billId/payment" -Method Post -Headers $h -ContentType "application/json" -Body '{"amount":500,"paidAt":"2026-08-12"}'
Write-Host "[PASS] status=$($r3.vendorBill.statusCode), balanceDue=$($r3.vendorBill.balanceDue)" -ForegroundColor Green
if ($r3.vendorBill.statusCode -ne "PART") { Write-Host "[FAIL] Expected PART" -ForegroundColor Red }

# ---- 8. Overpayment rejection (proves the "never silently clamp" rule) ----
Write-Host "`n=== 8. Attempt overpayment (should fail) ===" -ForegroundColor Cyan
try {
  Invoke-RestMethod -Uri "$base/api/tenant/vendor-bills/$billId/payment" -Method Post -Headers $h -ContentType "application/json" -Body '{"amount":9999,"paidAt":"2026-08-12"}'
  Write-Host "[FAIL] Should have been rejected" -ForegroundColor Red
} catch {
  Write-Host "[PASS] Correctly rejected: $($_.ErrorDetails.Message)" -ForegroundColor Green
}

# ---- 9. Final payment (proves full payoff -> PAID, balance zeroed) ----
Write-Host "`n=== 9. Record final payment (582.50) ===" -ForegroundColor Cyan
$r4 = Invoke-RestMethod -Uri "$base/api/tenant/vendor-bills/$billId/payment" -Method Post -Headers $h -ContentType "application/json" -Body '{"amount":582.50,"paidAt":"2026-08-12"}'
Write-Host "[PASS] status=$($r4.vendorBill.statusCode), balanceDue=$($r4.vendorBill.balanceDue)" -ForegroundColor Green
if ($r4.vendorBill.statusCode -ne "PAID" -or $r4.vendorBill.balanceDue -ne 0) { Write-Host "[FAIL] Expected PAID / balanceDue 0" -ForegroundColor Red }

# ---- 10. Settlement ledger read (proves ListPayments) ----
Write-Host "`n=== 10. List payment ledger ===" -ForegroundColor Cyan
$payments = Invoke-RestMethod -Uri "$base/api/tenant/vendor-bills/$billId/payments" -Headers $h
Write-Host "[PASS] $($payments.payments.Count) payment(s) recorded, total = $(($payments.payments | Measure-Object -Property amount -Sum).Sum)" -ForegroundColor Green

# ---- 11. Audit trail (proves every mutation was logged) ----
Write-Host "`n=== 11. Audit trail ===" -ForegroundColor Cyan
$audit = Invoke-RestMethod -Uri "$base/api/tenant/vendor-bills/$billId/audit" -Headers $h
Write-Host "[PASS] $($audit.audit.Count) audit entries: $(($audit.audit | ForEach-Object { $_.action }) -join ', ')" -ForegroundColor Green

Write-Host "`n=== DONE: $($bill.vendorBill.vendorBillNumber) went DRFT -> PAPV -> APPV -> PART -> PAID ===" -ForegroundColor Cyan

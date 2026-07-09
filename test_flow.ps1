# PowerShell Flow Integration Test Script for Medical IoT Backend

Write-Host "==============================================" -ForegroundColor Cyan
Write-Host "   Starting Medical IoT Backend Flow Test" -ForegroundColor Cyan
Write-Host "==============================================" -ForegroundColor Cyan

$baseUrl = "http://localhost:8080"
$mac = "11:22:33:44:55:66"
$sessionId = "sess_999"

# 1. Register User
Write-Host "`n[Step 1] Registering a new user..." -ForegroundColor Yellow
$regBody = @{
    phone = "0901234567"
    password = "Password123"
} | ConvertTo-Json

try {
    $regResponse = Invoke-RestMethod -Uri "$baseUrl/api/v1/auth/register" -Method Post -Body $regBody -ContentType "application/json"
    Write-Host "Register Success: $($regResponse.message)" -ForegroundColor Green
} catch {
    $err = $_.Exception.Message
    if ($_.Exception.Response) {
        $reader = New-Object System.IO.StreamReader($_.Exception.Response.GetResponseStream())
        $err = $reader.ReadToEnd()
    }
    Write-Host "Register Failed: $err" -ForegroundColor Red
    # Continue in case user already registered
}

# 2. Login User
Write-Host "`n[Step 2] Logging in user to obtain JWT..." -ForegroundColor Yellow
$loginBody = @{
    phone = "0901234567"
    password = "Password123"
} | ConvertTo-Json

$token = ""
try {
    $loginResponse = Invoke-RestMethod -Uri "$baseUrl/api/v1/auth/login" -Method Post -Body $loginBody -ContentType "application/json"
    $token = $loginResponse.token
    Write-Host "Login Success! Token obtained: $($token.Substring(0, 15))..." -ForegroundColor Green
} catch {
    $reader = New-Object System.IO.StreamReader($_.Exception.Response.GetResponseStream())
    $err = $reader.ReadToEnd()
    Write-Host "Login Failed: $err" -ForegroundColor Red
    Exit
}

# 3. Start Device Authorization (ESP32)
Write-Host "`n[Step 3] Initiating Device Authorization (ESP32 flow)..." -ForegroundColor Yellow
$authBody = @{
    mac_address = $mac
    session_id = $sessionId
} | ConvertTo-Json

$deviceCode = ""
$userCode = ""
try {
    $authResponse = Invoke-RestMethod -Uri "$baseUrl/api/v1/oauth/device/authorize" -Method Post -Body $authBody -ContentType "application/json"
    $deviceCode = $authResponse.device_code
    $userCode = $authResponse.user_code
    Write-Host "Device Auth Initiated!" -ForegroundColor Green
    Write-Host "  User Code  : $userCode" -ForegroundColor Cyan
    Write-Host "  Device Code: $deviceCode" -ForegroundColor Cyan
} catch {
    $reader = New-Object System.IO.StreamReader($_.Exception.Response.GetResponseStream())
    $err = $reader.ReadToEnd()
    Write-Host "Device Auth Failed: $err" -ForegroundColor Red
    Exit
}

# 4. Generate PIN PoP Signature
Write-Host "`n[Step 4] Computing cryptographic PIN PoP Signature..." -ForegroundColor Yellow
$secret = "pin-pop-secret-key"
$message = $userCode + ":" + $mac + ":" + $sessionId
$hmacsha = New-Object System.Security.Cryptography.HMACSHA256
$hmacsha.Key = [Text.Encoding]::ASCII.GetBytes($secret)
$signatureBytes = $hmacsha.ComputeHash([Text.Encoding]::ASCII.GetBytes($message))
$signature = [System.BitConverter]::ToString($signatureBytes).Replace("-", "").ToLower()
Write-Host "  Computed HMAC-SHA256: $signature" -ForegroundColor Cyan

# 5. Confirm Device Authorization (User App)
Write-Host "`n[Step 5] Confirming Device Authorization from App..." -ForegroundColor Yellow
$confirmBody = @{
    user_code = $userCode
    mac_address = $mac
    session_id = $sessionId
    pin_pop_signature = $signature
} | ConvertTo-Json

try {
    $headers = @{
        "Authorization" = "Bearer $token"
    }
    $confirmResponse = Invoke-RestMethod -Uri "$baseUrl/api/v1/oauth/device/confirm" -Method Post -Body $confirmBody -ContentType "application/json" -Headers $headers
    Write-Host "Confirmation Success: $($confirmResponse.message)" -ForegroundColor Green
} catch {
    $reader = New-Object System.IO.StreamReader($_.Exception.Response.GetResponseStream())
    $err = $reader.ReadToEnd()
    Write-Host "Confirmation Failed: $err" -ForegroundColor Red
    Exit
}

# 6. Exchange Device Code for Access Token (ESP32 completion)
Write-Host "`n[Step 6] Requesting token with Device Code..." -ForegroundColor Yellow
$tokenBody = @{
    device_code = $deviceCode
    mac_address = $mac
} | ConvertTo-Json

try {
    $tokenResponse = Invoke-RestMethod -Uri "$baseUrl/api/v1/oauth/token" -Method Post -Body $tokenBody -ContentType "application/json"
    Write-Host "Token Exchange Success!" -ForegroundColor Green
    Write-Host "  Access Token: $($tokenResponse.access_token)" -ForegroundColor Cyan
    Write-Host "  Token Type  : $($tokenResponse.token_type)" -ForegroundColor Cyan
} catch {
    $reader = New-Object System.IO.StreamReader($_.Exception.Response.GetResponseStream())
    $err = $reader.ReadToEnd()
    Write-Host "Token Exchange Failed: $err" -ForegroundColor Red
    Exit
}

Write-Host "`n==============================================" -ForegroundColor Green
Write-Host "   Flow Verification Completed Successfully!" -ForegroundColor Green
Write-Host "==============================================" -ForegroundColor Green

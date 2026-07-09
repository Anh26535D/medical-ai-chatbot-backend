# Medical IoT Backend

This is a high-performance, production-ready Golang backend built using the **Gin-Gonic** web framework. The system manages user registration & login, implements the **RFC 8628 OAuth 2.0 Device Authorization Grant Flow** for ESP32 devices, and runs a background worker subscribing to health telemetry data (BPM, SPO2) via an **EMQX MQTT Broker** saved via the MongoDB **Hourly Bucket Pattern**.

---

## 📂 Project Directory Structure

```text
├── database.go         # Mongo & Redis connection setup and DB service interface
├── models.go           # Struct models for database documents & API requests/responses
├── auth_handler.go     # Register & Login handlers (Bcrypt & 30-day JWT signature)
├── auth_test.go        # Unit tests for authentication endpoints
├── device_handler.go   # RFC 8628 Device Flow handlers (Authorize, Confirm, Token)
├── device_test.go      # Unit tests for RFC 8628 Flow
├── mqtt_worker.go      # Background Goroutine worker subscribing to EMQX telemetry
├── main.go             # Application entrypoint & Gin routers registration
├── Dockerfile          # Multi-stage optimized Docker build configuration
├── docker-compose.yml  # Orchestrates Backend App, MongoDB, Redis, and EMQX Broker
├── test_flow.ps1       # Automated PowerShell end-to-end integration test script
└── README.md           # Documentation
```

---

## 🚀 Getting Started

### 🐳 Run with Docker

To boot the entire stack (Go App, MongoDB, Redis, EMQX Broker) simultaneously:

```bash
docker compose up -d --build
```

- **API Endpoint:** `http://localhost:8080`
- **EMQX Broker Port:** `tcp://localhost:1883`
- **EMQX Web Dashboard:** `http://localhost:18083` (Default login: `admin` / `public`)

---

## 🧪 Testing

### 1. Run Unit Tests (TDD Mocking)
To run Go unit tests verifying authorization and registration logic in isolation:

```bash
go test -v ./...
```

### 2. Run End-to-End Integration Flow
To test the complete API workflow end-to-end (User Register ➔ Login ➔ Device Init ➔ Crypto PIN PoP Signature ➔ User App Approve ➔ Device Token Exchange):

```powershell
powershell -ExecutionPolicy Bypass -File .\test_flow.ps1
```

---

## 🔒 API Specifications

### 🔑 Authentication
- **`POST /api/v1/auth/register`**: Receives `{phone, password}`, hashes the password with BCrypt, and prevents duplicate registration.
- **`POST /api/v1/auth/login`**: Receives `{phone, password}`, validates credentials, and signs a 30-day JWT containing `uid_user`.

### 📱 RFC 8628 Device Flow
1. **`POST /api/v1/oauth/device/authorize`**: Initiated by ESP32 containing `{mac_address, session_id}`. Generates `device_code` (32 chars) and `user_code` (`AAAA-1111`). Cached in Redis (TTL = 300s). Emulates updates to Firebase RTDB.
2. **`POST /api/v1/oauth/device/confirm`**: Performed by the mobile app (authenticated with Bearer JWT). Receives `{user_code, mac_address, session_id, pin_pop_signature}`. Computes HMAC-SHA256 of `user_code:mac:session` using the PIN PoP secret to approve the session in Redis.
3. **`POST /api/v1/oauth/token`**: Polling request from ESP32. If approved, generates a permanent device token, pairs the device in MongoDB, and cleans up the temporary Redis cache.

---

## 📈 Health Telemetry Parsing (MQTT Worker)
The background worker connects to the EMQX broker and listens to the topic:
`devices/+/telemetry`

When it receives raw plain-text payload e.g. `75,98`:
1. Parses BPM (`75`) and SPO2 (`98`).
2. Evaluates the health status (`Normal` or `Warning` if BPM is outside 60-100 or SPO2 < 95%).
3. Upserts and saves the data inside MongoDB under an **Hourly Bucket Pattern** (`{mac_address}_{date}_{hour}`).
4. Emulates Realtime Database updates to Firebase RTDB at `/live_devices/{mac}/current`.

---

## 🔥 Real Firebase Admin SDK Setup Guide

For local testing, the backend emulates Firebase RTDB updates via stdout logs. To connect to a real Firebase Realtime Database using the official Go Admin SDK, follow these steps:

### 1. Generate Firebase Service Account Key
1. Open the [Firebase Console](https://console.firebase.google.com/).
2. Select your project, and navigate to **Project Settings ➔ Service Accounts**.
3. Under **Firebase Admin SDK**, click **Generate New Private Key**.
4. Save the downloaded JSON file as `service-account.json` in the root directory of this backend.

### 2. Add Firebase Admin Go SDK Dependency
In your project directory, run:
```bash
go get firebase.google.com/go/v4
```

### 3. Initialize Firebase in Code
You can replace the simulated functions in `device_handler.go` and `mqtt_worker.go` with actual calls using this initialization block in `main.go`:

```go
import (
	"context"
	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/option"
)

var firebaseApp *firebase.App

func InitFirebase() {
	opt := option.WithCredentialsFile("service-account.json")
	config := &firebase.Config{
		DatabaseURL: "https://<YOUR_PROJECT_ID>-default-rtdb.firebaseio.com/",
	}
	app, err := firebase.NewApp(context.Background(), config, opt)
	if err != nil {
		log.Fatalf("Error initializing Firebase App: %v", err)
	}
	firebaseApp = app
}
```

### 4. Write to Realtime Database
Get a client reference and write values asynchronously:
```go
func UpdateFirebaseLive(mac string, bpm, spo2 int, status string) {
	ctx := context.Background()
	client, err := firebaseApp.Database(ctx)
	if err != nil {
		log.Printf("Firebase DB client error: %v", err)
		return
	}
	
	ref := client.NewRef(fmt.Sprintf("devices/%s/latest", mac))
	data := map[string]interface{}{
		"bpm":        bpm,
		"spo2":       spo2,
		"status":     status,
		"updated_at": time.Now().Unix(),
	}
	
	if err := ref.Set(ctx, data); err != nil {
		log.Printf("Error updating Firebase: %v", err)
	}
}
```

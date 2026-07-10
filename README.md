# Medical IoT Backend

This is a high-performance, production-ready Golang backend built using the **Gin-Gonic** web framework. The system manages user registration & login, implements the **RFC 8628 OAuth 2.0 Device Authorization Grant Flow** for ESP32 devices, and runs a background worker subscribing to health telemetry data (BPM, SPO2, Temperature, Humidity) via an **EMQX MQTT Broker** saved via the MongoDB **Hourly Bucket Pattern** and synchronized in real-time to **Firebase**.

---

## 📂 Project Directory Structure

```text
├── database.go         # Mongo & Redis connection setup and DB service interface
├── database_test.go    # TDD database tests using MongoDB mtest mocking framework
├── firebase.go         # Structured client layout ready for Firebase RTDB integration
├── models.go           # Struct models for database documents & API requests/responses
├── auth_handler.go     # Register & Login handlers (Bcrypt & 30-day JWT signature)
├── auth_test.go        # Unit tests for authentication endpoints
├── device_handler.go   # RFC 8628 Device Flow handlers (Authorize, Confirm, Token)
├── device_test.go      # Unit tests for RFC 8628 Flow
├── mqtt_worker.go      # Background Goroutine worker subscribing to EMQX telemetry
├── mqtt_worker_test.go # TDD worker tests verifying parsing, threshold rules, and callbacks
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
To run Go unit tests verifying authorization, registration logic, telemetry parsing, thresholds, and MongoDB operations in isolation:

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
1. **`POST /api/v1/oauth/device/authorize`**: Initiated by ESP32 containing `{mac_address, session_id}`. Generates `device_code` (32 chars) and `user_code` (`AAAA-1111`). Cached in Redis (TTL = 300s). updates Firebase RTDB polling node.
2. **`POST /api/v1/oauth/device/confirm`**: Performed by the mobile app (authenticated with Bearer JWT). Receives `{user_code, mac_address, session_id, pin_pop_signature}`. Computes HMAC-SHA256 of `user_code:mac:session` using the PIN PoP secret to approve the session in Redis.
3. **`POST /api/v1/oauth/token`**: Polling request from ESP32. If approved, generates a permanent device token, pairs the device in MongoDB, and cleans up the temporary Redis cache.

---

## 📈 Health Telemetry Ingestion (MQTT Worker)
The background worker connects to the EMQX broker and listens to the topic:
`devices/+/telemetry`

When it receives raw plain-text payload containing 4 compressed fields, e.g., `bpm,spo2,temp,hum` (e.g., `"75,98,32.5,65.0"`):
1. **Parses 4 fields**: BPM (`75`), SPO2 (`98`), Temperature (`32.5`), Humidity (`65.0`).
2. **Evaluates Clinical Status**: Determines status (`Normal` or `Warning`). A `Warning` status is triggered if:
   - BPM is outside the range 60-100.
   - SPO2 is below 95%.
   - **Temperature exceeds the alert threshold of 39.0°C**.
3. **Persists to MongoDB**: Upserts and appends data inside MongoDB using an **Hourly Bucket Pattern** (`{mac_address}_{date}_{hour}`) via `$push` and `$setOnInsert`.
4. **Real-time Firebase Update**: Updates the Realtime Database at the latest node path: `devices/{mac}/telemetry/latest`.

---

## 🔥 Firebase Realtime Database Integration

The backend supports updating Firebase RTDB via a REST client architecture using:
- Target Database URL configured via `FIREBASE_DATABASE_URL` environment variable.
- Optional Authorization Token configured via `FIREBASE_AUTH_TOKEN` environment variable.

### Production Realtime Database Sync
For production environments, data is sent to the target node paths:
- Telemetry Updates: `devices/{mac}/telemetry/latest.json`
- Provisioning Flow Updates: `provisioning_polling/{mac}_{sessionId}.json`


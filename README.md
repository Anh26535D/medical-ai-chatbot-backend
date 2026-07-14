# Medical IoT Backend

This is a high-performance, production-ready Golang backend built using the **Gin-Gonic** web framework. The system manages user registration & login, implements the **RFC 8628 OAuth 2.0 Device Authorization Grant Flow** for ESP32 devices, and runs a background worker subscribing to health telemetry data (BPM, SPO2, Temperature, Humidity) via an **EMQX MQTT Broker** saved via the MongoDB **Hourly Bucket Pattern** and synchronized in real-time to **Firebase**.

---

## 📂 Project Directory Structure

```text
├── cmd/
│   └── server/
│       └── main.go             # Application entrypoint & Gin router setup
├── internal/
│   ├── model/
│   │   └── models.go           # Struct models for DB documents & API payloads
│   ├── repository/
│   │   ├── database.go         # MongoDB & Redis concrete implementations
│   │   ├── database_test.go    # MongoDB mtest database tests
│   │   └── firebase.go         # Real-time Firebase RTDB integrations
│   ├── handler/
│   │   ├── auth.go             # Register & Login REST handlers
│   │   ├── auth_test.go        # Unit tests for authentication handlers
│   │   ├── device.go           # RFC 8628 Device Flow handlers
│   │   └── device_test.go      # Unit tests for Device Flow handlers
│   └── worker/
│       ├── mqtt_worker.go      # Background MQTT subscriber
│       └── mqtt_worker_test.go # TDD tests for telemetry parsing & thresholds
├── Dockerfile                  # Multi-stage optimized Docker build configuration
├── docker-compose.yml          # Orchestrates app, MongoDB, Redis, and EMQX
├── test_flow.ps1               # Automated end-to-end integration script
└── README.md                   # Documentation
```

---

## 🚀 Getting Started

### 🐳 Run with Docker

To boot the entire stack (Go App, MongoDB, Redis, EMQX Broker) simultaneously:

```bash
docker compose up -d --build
```

- **API Endpoint:** `http://localhost:8080` (or `http://192.168.1.41:8080` in local network)
- **EMQX Broker Port (plain TCP, internal use only):** `tcp://localhost:1883`
- **EMQX Broker Port (TLS, for real devices):** `tls://localhost:8883` — requires the certs in `certs/` (see below)
- **EMQX Web Dashboard:** `http://localhost:18083` (Default login: `admin` / `public`)

### 🔐 TLS Certificates (`certs/`)

The `certs/` directory holds the CA and server certificate/key EMQX uses for its TLS listener on port 8883:
- `ca.crt` / `ca.key` — self-signed CA (`CN=MedicalIOTCA`).
- `server.crt` / `server.key` — leaf cert signed by the CA, `CN=medical-iot-emqx`, generated from `san.cnf`.

The firmware connects by IP (no DNS on the LAN), but the bundled ESP32 mbedTLS (2.28.x) only matches TLS hostnames against `DNS:` type SAN entries, not `IP:` type. `san.cnf` therefore duplicates each IP as both an `IP:` entry (for normal TLS clients) and a `DNS:` entry (for the ESP32). If you change the broker's IP or regenerate the cert, update `san.cnf` (e.g., adding `192.168.1.41`) and re-run:
```bash
openssl req -new -key certs/server.key -out certs/server.csr -config certs/san.cnf
openssl x509 -req -in certs/server.csr -CA certs/ca.crt -CAkey certs/ca.key -CAcreateserial -out certs/server.crt -days 3650 -sha256 -extfile certs/san.cnf -extensions v3_req
docker compose restart emqx
```
Also keep the firmware's embedded `ca_cert` (in `iot-firmware/src/main.cpp`) in sync with `certs/ca.crt`.

### ⚠️ EMQX HTTP auth/ACL webhook config caveat

EMQX's env-var overrides (`EMQX_AUTHENTICATION__1__BODY__*`, `EMQX_AUTHORIZATION__SOURCES__1__BODY__*` in `docker-compose.yml`) reliably set scalar fields (`url`, `method`, `backend`) but **silently fail to apply nested map fields** like the webhook request `body` — EMQX 5.3 just omits it, so the webhook POSTs an empty payload to `/api/v1/mqtt/auth` / `/api/v1/mqtt/acl`, and every client (including the backend's own MQTT worker) gets denied.

If you ever need to reconfigure this, don't fight the env-var path — set it via the EMQX REST API instead (this persists in the `emqx_data` volume across restarts):
```bash
# Login to EMQX to retrieve Bearer Token
TOKEN=$(curl -s -X POST http://localhost:18083/api/v5/login -H "Content-Type: application/json" -d '{"username":"admin","password":"public"}' | jq -r .token)

# Configure HTTP Authentication Webhook
curl -s -X PUT http://localhost:18083/api/v5/authentication/password_based:http -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{
  "mechanism": "password_based", "backend": "http", "method": "post",
  "url": "http://app:8080/api/v1/mqtt/auth",
  "headers": {"content-type": "application/json"},
  "body": {"clientid": "${clientid}", "username": "${username}", "password": "${password}"}
}'

# Configure HTTP Authorization (ACL) Webhook
curl -s -X PUT http://localhost:18083/api/v5/authorization/sources/http -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{
  "type": "http", "method": "post",
  "url": "http://app:8080/api/v1/mqtt/acl",
  "headers": {"content-type": "application/json"},
  "body": {"clientid": "${clientid}", "username": "${username}", "topic": "${topic}", "action": "${action}"}
}'
```
(Same idea for `/api/v5/authorization/sources/http` with `topic`/`action` in the body.) You can verify what EMQX actually resolved with `GET /api/v5/authentication`.

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
- **`POST /api/v1/auth/login`**: Receives `{phone, password}`, validates credentials, and returns:
  - `token`: a 30-day backend JWT containing `uid_user`, used to authenticate to this API.
  - `firebase_token`: a **Firebase Auth custom token** (RS256, signed with the service account key), minted with `uid = user.ID`. The client must call `FirebaseAuth.signInWithCustomToken(firebase_token)` — this makes `FirebaseAuth.currentUser.uid` equal this backend's own user ID, which the RTDB security rules and the `users/{uid}/...` paths both depend on. Without this step, the client's Firebase Auth UID (e.g. an anonymous sign-in) will never match the path this backend writes telemetry to, and the app will never see any data despite everything else working.
  - `expires_at`: unix timestamp for the backend JWT's expiry.

### 📱 RFC 8628 Device Flow
1. **`POST /api/v1/oauth/device/authorize`**: Initiated by ESP32 containing `{mac_address, session_id}`. Generates `device_code` (32 chars) and `user_code` (`AAAA-1111`). Cached in Redis (TTL = 300s). Writes `{UserCode, PairingNonce, CreatedAt}` to the Firebase RTDB polling node `provisioning_polling/{mac}_{session_id}`.
2. **`POST /api/v1/oauth/device/confirm`**: Performed by the mobile app (authenticated with Bearer JWT). Receives `{user_code, mac_address, session_id, pin_pop_signature}`. Computes HMAC-SHA256 of `user_code:mac:session` using the PIN PoP secret to approve the session in Redis.
3. **`POST /api/v1/oauth/token`**: Polling request from ESP32. If approved, generates a permanent device token, pairs the device in MongoDB, and cleans up both the temporary Redis cache **and** the Firebase `provisioning_polling` entry.

### 📡 EMQX HTTP Auth/ACL Webhooks (internal — called by EMQX, not clients)
- **`POST /api/v1/mqtt/auth`**: Called by EMQX on every MQTT `CONNECT`. Receives `{clientid, username, password}`. For real devices, `clientid`/`username` is the MAC and `password` is the device's access token, checked against MongoDB. The backend's own MQTT worker authenticates here too, via a separate shared-secret identity (`MQTTWorkerClientID`/`MQTTWorkerSecret`, env `MQTT_WORKER_SECRET`) rather than a per-device token lookup.
- **`POST /api/v1/mqtt/acl`**: Called by EMQX on every `PUBLISH`/`SUBSCRIBE`. Devices may only publish to their own `devices/{mac}/telemetry`; the backend's MQTT worker identity may subscribe to the wildcard `devices/+/telemetry`.

### 📴 Device Management
- **`DELETE /api/v1/devices/{mac}`**: Unpairs a device (Bearer JWT required) — removes it from MongoDB and clears its Firebase ownership mapping, forcing it to re-run the device flow to reconnect.

---

## 📈 Health Telemetry Ingestion (MQTT Worker)
The backend runs its own MQTT client (`internal/worker/mqtt_worker.go`) that connects to the broker (plain `tcp://emqx:1883`, internal Docker network only) and subscribes to the topic:
`devices/+/telemetry`

Note: this connection goes through the same `ConnectRetry`-driven retry loop as any other client, since the auth webhook it authenticates against is served by this same backend process — the very first connect attempt at startup is expected to fail (the HTTP server isn't listening yet at that point in `main.go`) and is retried automatically every 5s until it succeeds.

When it receives raw plain-text payload containing 4 compressed fields, e.g., `bpm,spo2,temp,hum` (e.g., `"75,98,32.5,65.0"`):
1. **Parses 4 fields**: BPM (`75`), SPO2 (`98`), Temperature (`32.5`), Humidity (`65.0`).
2. **Evaluates Clinical Status**: Determines status (`Normal` or `Warning`). A `Warning` status is triggered if:
   - BPM is outside the range 60-100.
   - SPO2 is below 95%.
   - **Temperature exceeds the alert threshold of 39.0°C**.
3. **Persists to MongoDB**: Upserts and appends data inside MongoDB using an **Hourly Bucket Pattern** (`{mac_address}_{date}_{hour}`) via `$push` and `$setOnInsert`.
4. **Real-time Firebase Update**: Looks up the device's `OwnerUID` in MongoDB, then updates the Realtime Database at `users/{ownerUID}/devices/{mac}/telemetry/latest`.

---

## 🔥 Firebase Realtime Database Integration

The backend updates Firebase RTDB via a hand-rolled REST client (`internal/repository/firebase.go`), authenticating with the mounted service account (`FIREBASE_KEY_PATH`) via a standard Google OAuth2 access token — **not** the official Firebase Admin SDK. This service account also has sufficient IAM role to bypass RTDB security rules (the same "editor"-level role Firebase grants its default `firebase-adminsdk-*` service account), so backend writes always succeed regardless of the rules below.

- Target Database URL configured via `FIREBASE_DATABASE_URL` environment variable.
- Service account JSON path configured via `FIREBASE_KEY_PATH` (mounted read-only in `docker-compose.yml`).

### Node paths
- Live telemetry: `users/{ownerUID}/devices/{mac}/telemetry/latest`
- Historical telemetry: `users/{ownerUID}/devices/{mac}/history` (see `MqttAclHandler`/Android's `historyEventListener` — not written by the current backend, reserved for future use)
- Device flow polling: `provisioning_polling/{mac}_{sessionId}` (includes `CreatedAt` unix timestamp so consumers can ignore stale entries — RTDB has no native TTL)
- Device ownership (for security rules): `device_ownership/{mac}`

### Security rules
```json
{
  "rules": {
    "users": {
      "$uid": { ".read": "auth != null && auth.uid === $uid", ".write": false }
    },
    "device_ownership": {
      "$mac": { ".read": "auth != null && data.val() === auth.uid", ".write": false }
    },
    "provisioning_polling": {
      ".read": "auth != null", ".write": "auth != null"
    }
  }
}
```
`users/$uid` and `device_ownership/$mac` are `.write: false` for everyone except this backend's admin-bypass service account — clients only ever read them. `auth.uid === $uid` is why the `firebase_token`/`signInWithCustomToken` step at login (above) is required: without it, a client's Firebase Auth UID has no relationship to `$uid` and every read is denied.

---

## 🌐 Production Deployment Guide

To deploy the application to a production Linux server (e.g., Ubuntu VPS), follow these steps:

### 1. Prerequisites
Ensure Docker and Docker Compose are installed on your server:
```bash
sudo apt update && sudo apt upgrade -y
curl -fsSL https://get.docker.com -o get-docker.sh
sudo sh get-docker.sh
```

### 2. Project Setup
Clone the repository and set up your configurations:
```bash
git clone <your-repository-url>
cd medical-ai-chatbot-backend
cp .env.example .env
nano .env # Configure Firebase parameters and system credentials
```

### 3. Start the Stack
Boot up the backend along with MongoDB, Redis, and EMQX in detached background mode:
```bash
docker compose --env-file .env up -d --build
```

### 4. Firewall Settings
Open the ports required for the API and telemetry ingestion:
```bash
sudo ufw allow 8080/tcp # HTTP REST API
sudo ufw allow 8883/tcp # MQTT over TLS (real devices connect here, not 1883)
```
Port `1883` (plain MQTT) does not need to be exposed externally — it's only used internally between the backend's MQTT worker and EMQX on the Docker network.



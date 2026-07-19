package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"medical-iot-backend/internal/model"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/oauth2/google"
)

// FirebaseClient handles updating the Firebase Realtime Database.
type FirebaseClient struct {
	DatabaseURL string
	jsonKeyPath string
	HTTPClient  *http.Client
}

// Global Firebase instance
var Firebase *FirebaseClient

// InitFirebase initializes the global Firebase client using environment variables.
func InitFirebase() {
	dbURL := os.Getenv("FIREBASE_DATABASE_URL")
	if dbURL == "" {
		dbURL = "https://caromaster-default-rtdb.asia-southeast1.firebasedatabase.app" // fallback default
	}
	dbURL = strings.TrimSuffix(dbURL, "/")
	keyPath := os.Getenv("FIREBASE_KEY_PATH")
	Firebase = &FirebaseClient{
		DatabaseURL: dbURL,
		jsonKeyPath: keyPath,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Helper to get OAuth2 access token dynamically from the service account JSON
func (fc *FirebaseClient) getAccessToken(ctx context.Context) (string, error) {
	if fc.jsonKeyPath == "" {
		return "", fmt.Errorf("jsonKeyPath not configured")
	}
	data, err := os.ReadFile(fc.jsonKeyPath)
	if err != nil {
		return "", err
	}
	conf, err := google.JWTConfigFromJSON(data,
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/firebase.database",
	)
	if err != nil {
		return "", err
	}
	ts := conf.TokenSource(ctx)
	tok, err := ts.Token()
	if err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// MintCustomToken signs a Firebase Auth custom token for the given uid using this
// service account's private key. Signing the app's own backend user ID (not a Firebase
// UID) into the token is what makes FirebaseAuth's client-side uid equal our own user ID
// after the client calls signInWithCustomToken, which the RTDB rules and the
// users/{uid}/... paths both depend on.
//
// Uses jwt.MapClaims instead of jwt.RegisteredClaims because RegisteredClaims always
// marshals "aud" as a JSON array; Firebase's custom-token verifier requires "aud" to be
// a bare string and rejects an array form with INVALID_CUSTOM_TOKEN.
func (fc *FirebaseClient) MintCustomToken(uid string) (string, error) {
	if fc.jsonKeyPath == "" {
		return "", fmt.Errorf("jsonKeyPath not configured")
	}
	data, err := os.ReadFile(fc.jsonKeyPath)
	if err != nil {
		return "", err
	}
	var sa struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
	}
	if err := json.Unmarshal(data, &sa); err != nil {
		return "", err
	}
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(sa.PrivateKey))
	if err != nil {
		return "", err
	}

	now := time.Now()
	claims := jwt.MapClaims{
		"uid": uid,
		"iss": sa.ClientEmail,
		"sub": sa.ClientEmail,
		"aud": "https://identitytoolkit.googleapis.com/google.identity.identitytoolkit.v1.IdentityToolkit",
		"iat": now.Unix(),
		"exp": now.Add(1 * time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(key)
}

// UpdateLiveTelemetry updates the latest telemetry data for a device at users/{uid}/devices/{mac}/telemetry/latest.
func (fc *FirebaseClient) UpdateLiveTelemetry(ctx context.Context, ownerUID string, mac string, point model.TelemetryDataPoint) error {
	if fc == nil || fc.DatabaseURL == "" {
		return fmt.Errorf("firebase client not initialized")
	}
	url := fmt.Sprintf("%s/users/%s/devices/%s/telemetry/latest.json", fc.DatabaseURL, ownerUID, mac)
	token, err := fc.getAccessToken(ctx)
	if err == nil && token != "" {
		url = fmt.Sprintf("%s?access_token=%s", url, token)
	}

	data, err := json.Marshal(point)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := fc.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("failed to write telemetry to firebase: status code %d", resp.StatusCode)
	}
	return nil
}

// UpdateDeviceStatus updates the online/offline connection state at users/{uid}/devices/{mac}/status.json
func (fc *FirebaseClient) UpdateDeviceStatus(ctx context.Context, ownerUID string, mac string, isOnline bool) error {
	if fc == nil || fc.DatabaseURL == "" {
		return fmt.Errorf("firebase client not initialized")
	}
	url := fmt.Sprintf("%s/users/%s/devices/%s/status.json", fc.DatabaseURL, ownerUID, mac)
	token, err := fc.getAccessToken(ctx)
	if err == nil && token != "" {
		url = fmt.Sprintf("%s?access_token=%s", url, token)
	}

	statusMap := map[string]interface{}{
		"online":    isOnline,
		"last_seen": time.Now().Unix(),
	}

	data, err := json.Marshal(statusMap)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := fc.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("failed to write status to firebase: status code %d", resp.StatusCode)
	}
	return nil
}

// UpdateProvisioningStatus updates the pairing credentials/flow information for polling.
func (fc *FirebaseClient) UpdateProvisioningStatus(ctx context.Context, mac string, sessionId string, userCode string, pairingNonce string) error {
	if fc == nil || fc.DatabaseURL == "" {
		return fmt.Errorf("firebase client not initialized")
	}
	url := fmt.Sprintf("%s/provisioning_polling/%s_%s.json", fc.DatabaseURL, mac, sessionId)
	token, err := fc.getAccessToken(ctx)
	if err == nil && token != "" {
		url = fmt.Sprintf("%s?access_token=%s", url, token)
	}

	payload := map[string]interface{}{
		"UserCode":     userCode,
		"PairingNonce": pairingNonce,
		// Firebase RTDB has no native TTL; clients use this to ignore stale entries that
		// were never cleaned up (e.g. the polling security rules block client-side
		// removeValue(), so entries can outlive their Redis-side session expiry).
		"CreatedAt": time.Now().Unix(),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := fc.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("failed to write provisioning status to firebase: status code %d", resp.StatusCode)
	}
	return nil
}

// DeleteProvisioningStatus deletes the pairing polling credentials from Firebase.
func (fc *FirebaseClient) DeleteProvisioningStatus(ctx context.Context, mac string, sessionId string) error {
	if fc == nil || fc.DatabaseURL == "" {
		return fmt.Errorf("firebase client not initialized")
	}
	url := fmt.Sprintf("%s/provisioning_polling/%s_%s.json", fc.DatabaseURL, mac, sessionId)
	token, err := fc.getAccessToken(ctx)
	if err == nil && token != "" {
		url = fmt.Sprintf("%s?access_token=%s", url, token)
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}

	resp, err := fc.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("failed to delete provisioning status in firebase: status code %d", resp.StatusCode)
	}
	return nil
}

// SetDeviceOwnership writes device ownership mapping for Firebase Security Rules.
// Allows the rule: users.$uid.read = "auth.uid === $uid"
func (fc *FirebaseClient) SetDeviceOwnership(ctx context.Context, ownerUID string, mac string) error {
	if fc == nil || fc.DatabaseURL == "" {
		return fmt.Errorf("firebase client not initialized")
	}
	url := fmt.Sprintf("%s/device_ownership/%s.json", fc.DatabaseURL, mac)
	token, err := fc.getAccessToken(ctx)
	if err == nil && token != "" {
		url = fmt.Sprintf("%s?access_token=%s", url, token)
	}

	data, err := json.Marshal(ownerUID)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := fc.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("failed to set device ownership in firebase: status code %d", resp.StatusCode)
	}
	return nil
}

// DeleteDeviceOwnership removes device ownership mapping from Firebase.
func (fc *FirebaseClient) DeleteDeviceOwnership(ctx context.Context, mac string) error {
	if fc == nil || fc.DatabaseURL == "" {
		return fmt.Errorf("firebase client not initialized")
	}
	url := fmt.Sprintf("%s/device_ownership/%s.json", fc.DatabaseURL, mac)
	token, err := fc.getAccessToken(ctx)
	if err == nil && token != "" {
		url = fmt.Sprintf("%s?access_token=%s", url, token)
	}

	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}

	resp, err := fc.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("failed to delete device ownership in firebase: status code %d", resp.StatusCode)
	}
	return nil
}

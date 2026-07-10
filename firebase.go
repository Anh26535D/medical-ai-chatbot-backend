package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// FirebaseClient handles updating the Firebase Realtime Database.
type FirebaseClient struct {
	DatabaseURL string
	AuthToken   string // For REST API authorization
	HTTPClient  *http.Client
}

// Global Firebase instance
var Firebase *FirebaseClient

// InitFirebase initializes the global Firebase client using environment variables.
func InitFirebase() {
	dbURL := os.Getenv("FIREBASE_DATABASE_URL")
	if dbURL == "" {
		dbURL = "https://medical-ai-chatbot-default-rtdb.firebaseio.com" // fallback default
	}
	authToken := os.Getenv("FIREBASE_AUTH_TOKEN")
	Firebase = &FirebaseClient{
		DatabaseURL: dbURL,
		AuthToken:   authToken,
		HTTPClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// UpdateLiveTelemetry updates the latest telemetry data for a device at devices/{mac}/telemetry/latest.
func (fc *FirebaseClient) UpdateLiveTelemetry(ctx context.Context, mac string, point TelemetryDataPoint) error {
	if fc == nil || fc.DatabaseURL == "" {
		return fmt.Errorf("firebase client not initialized")
	}
	url := fmt.Sprintf("%s/devices/%s/telemetry/latest.json", fc.DatabaseURL, mac)
	if fc.AuthToken != "" {
		url = fmt.Sprintf("%s?auth=%s", url, fc.AuthToken)
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

// UpdateProvisioningStatus updates the pairing credentials/flow information for polling.
func (fc *FirebaseClient) UpdateProvisioningStatus(ctx context.Context, mac string, sessionId string, userCode string, deviceCode string) error {
	if fc == nil || fc.DatabaseURL == "" {
		return fmt.Errorf("firebase client not initialized")
	}
	url := fmt.Sprintf("%s/provisioning_polling/%s_%s.json", fc.DatabaseURL, mac, sessionId)
	if fc.AuthToken != "" {
		url = fmt.Sprintf("%s?auth=%s", url, fc.AuthToken)
	}

	payload := map[string]string{
		"UserCode":   userCode,
		"DeviceCode": deviceCode,
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

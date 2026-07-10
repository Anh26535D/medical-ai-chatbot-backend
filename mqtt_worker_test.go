package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// We mock mqtt.Message interface
type mockMQTTMessage struct {
	topic   string
	payload []byte
}

func (m *mockMQTTMessage) Duplicate() bool   { return false }
func (m *mockMQTTMessage) Qos() byte         { return 0 }
func (m *mockMQTTMessage) Retained() bool    { return false }
func (m *mockMQTTMessage) Topic() string     { return m.topic }
func (m *mockMQTTMessage) MessageID() uint16 { return 0 }
func (m *mockMQTTMessage) Payload() []byte   { return m.payload }
func (m *mockMQTTMessage) Ack()              {}

func TestEvaluateStatus(t *testing.T) {
	tests := []struct {
		name     string
		bpm      int
		spo2     int
		temp     float64
		expected string
	}{
		{"Normal Status", 75, 98, 36.5, "Normal"},
		{"BPM Low Warning", 55, 98, 36.5, "Warning"},
		{"BPM High Warning", 105, 98, 36.5, "Warning"},
		{"SPO2 Warning", 75, 94, 36.5, "Warning"},
		{"Temp Alert Warning", 75, 98, 39.1, "Warning"},
		{"Temp Borderline Normal", 75, 98, 39.0, "Normal"},
		{"Multiple Warnings", 50, 90, 40.0, "Warning"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := evaluateStatus(tt.bpm, tt.spo2, tt.temp)
			assert.Equal(t, tt.expected, status)
		})
	}
}

func TestHandleTelemetryMessage_Success(t *testing.T) {
	// 1. Setup mock database
	mockDB := new(MockDatabase)
	DB = mockDB

	mac := "00:11:22:33:44:55"
	topic := "devices/" + mac + "/telemetry"
	payload := "75,98,36.5,65.0"

	// Expect DB call
	mockDB.On("UpdateTelemetryHistory", mock.Anything, mac, mock.Anything, mock.Anything, mock.MatchedBy(func(p TelemetryDataPoint) bool {
		return p.BPM == 75 && p.SPO2 == 98 && p.Temperature == 36.5 && p.Humidity == 65.0 && p.Status == "Normal"
	})).Return(nil)

	// 2. Setup mock Firebase HTTP server
	var firebaseReceivedPayload TelemetryDataPoint
	var firebaseReceivedURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firebaseReceivedURL = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &firebaseReceivedPayload)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Initialize Firebase global variable with the mock server URL
	Firebase = &FirebaseClient{
		DatabaseURL: server.URL,
		HTTPClient:  server.Client(),
	}

	// 3. Trigger telemetry handler
	msg := &mockMQTTMessage{
		topic:   topic,
		payload: []byte(payload),
	}
	handleTelemetryMessage(context.Background(), msg)

	// Assertions
	mockDB.AssertExpectations(t)
	assert.Equal(t, "/devices/"+mac+"/telemetry/latest.json", firebaseReceivedURL)
	assert.Equal(t, 75, firebaseReceivedPayload.BPM)
	assert.Equal(t, 98, firebaseReceivedPayload.SPO2)
	assert.Equal(t, 36.5, firebaseReceivedPayload.Temperature)
	assert.Equal(t, 65.0, firebaseReceivedPayload.Humidity)
	assert.Equal(t, "Normal", firebaseReceivedPayload.Status)
}

func TestHandleTelemetryMessage_InvalidPayload(t *testing.T) {
	mockDB := new(MockDatabase)
	DB = mockDB

	// Invalid payloads should not call DB or Firebase
	invalidPayloads := []string{
		"75,98",          // missing temp/hum
		"75,98,abc,65.0", // non-numeric temp
		"75,98,36.5",     // missing hum
	}

	for _, payload := range invalidPayloads {
		t.Run("Payload:"+payload, func(t *testing.T) {
			msg := &mockMQTTMessage{
				topic:   "devices/00:11:22:33:44:55/telemetry",
				payload: []byte(payload),
			}
			handleTelemetryMessage(context.Background(), msg)
			// DB methods shouldn't be called, asserting no mock calls
			mockDB.AssertNotCalled(t, "UpdateTelemetryHistory")
		})
	}
}

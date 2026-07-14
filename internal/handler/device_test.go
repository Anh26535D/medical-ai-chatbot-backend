package handler

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"medical-iot-backend/internal/model"
	"medical-iot-backend/internal/repository"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// Helper to compute PoP signature in tests
func testComputePinPopSignature(userCode, macAddress, sessionId string) string {
	h := hmac.New(sha256.New, []byte("pin-pop-secret-key"))
	h.Write([]byte(userCode + ":" + macAddress + ":" + sessionId))
	return hex.EncodeToString(h.Sum(nil))
}

func SetupDeviceRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/oauth/device/authorize", DeviceAuthorizeHandler)
	r.POST("/api/v1/oauth/device/confirm", DeviceConfirmHandler)
	r.POST("/api/v1/oauth/token", DeviceTokenHandler)
	r.POST("/api/v1/mqtt/auth", MqttAuthHandler)
	r.POST("/api/v1/mqtt/acl", MqttAclHandler)
	r.DELETE("/api/v1/devices/:mac", DeviceUnpairHandler)
	return r
}

func TestAuthorize_Success(t *testing.T) {
	mockDB := new(MockDatabase)
	repository.DB = mockDB

	payload := model.DeviceAuthorizePayload{
		MACAddress: "00:11:22:33:44:55",
		SessionID:  "session-abc-123",
	}
	body, _ := json.Marshal(payload)

	mockDB.On("SetDeviceFlow", mock.Anything, mock.Anything, mock.Anything, 300*time.Second).Return(nil)

	r := SetupDeviceRouter()
	req, _ := http.NewRequest("POST", "/api/v1/oauth/device/authorize", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)

	assert.Contains(t, resp, "device_code")
	assert.Contains(t, resp, "user_code")
	assert.Contains(t, resp, "verification_uri")

	deviceCode := resp["device_code"].(string)
	userCode := resp["user_code"].(string)

	assert.Equal(t, 32, len(deviceCode))
	matched, _ := regexp.MatchString("^[A-Z]{4}-\\d{4}$", userCode)
	assert.True(t, matched, "user_code format should be AAAA-1111")

	mockDB.AssertExpectations(t)
}

func TestConfirm_InvalidSignature(t *testing.T) {
	mockDB := new(MockDatabase)
	repository.DB = mockDB

	// Generate a valid JWT
	claims := &Claims{
		UIDUser: "user-123",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString(JWTSecret)

	payload := model.DeviceConfirmPayload{
		UserCode:        "ABCD-1234",
		MACAddress:      "00:11:22:33:44:55",
		SessionID:       "session-abc-123",
		PinPopSignature: "invalid-signature",
	}
	body, _ := json.Marshal(payload)

	session := &model.DeviceFlowSession{
		DeviceCode:   "devicecode123",
		MACAddress:   "00:11:22:33:44:55",
		SessionID:    "session-abc-123",
		PairingNonce: "pin-pop-secret-key",
		Status:       "authorization_pending",
	}
	mockDB.On("GetDeviceFlow", mock.Anything, "ABCD-1234").Return(session, nil)

	r := SetupDeviceRouter()
	req, _ := http.NewRequest("POST", "/api/v1/oauth/device/confirm", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "Invalid PIN PoP signature", resp["error"])
}

func TestConfirm_Success(t *testing.T) {
	mockDB := new(MockDatabase)
	repository.DB = mockDB

	claims := &Claims{
		UIDUser: "user-123",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString(JWTSecret)

	userCode := "ABCD-1234"
	mac := "00:11:22:33:44:55"
	sessionID := "session-abc-123"
	validSig := ComputePinPopSignature(userCode, mac, sessionID)

	payload := model.DeviceConfirmPayload{
		UserCode:        userCode,
		MACAddress:      mac,
		SessionID:       sessionID,
		PinPopSignature: validSig,
	}
	body, _ := json.Marshal(payload)

	session := &model.DeviceFlowSession{
		DeviceCode:   "devicecode1234567890123456789012",
		MACAddress:   mac,
		UIDESP:       "esp-32-id",
		SessionID:    sessionID,
		Status:       "authorization_pending",
		PairingNonce: "pin-pop-secret-key",
	}

	mockDB.On("GetDeviceFlow", mock.Anything, userCode).Return(session, nil)
	mockDB.On("SetDeviceFlow", mock.Anything, userCode, mock.MatchedBy(func(s *model.DeviceFlowSession) bool {
		return s.Status == "approved" && s.MACAddress == mac && s.SessionID == sessionID
	}), 300*time.Second).Return(nil)

	r := SetupDeviceRouter()
	req, _ := http.NewRequest("POST", "/api/v1/oauth/device/confirm", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "Device authorization approved successfully", resp["message"])
	mockDB.AssertExpectations(t)
}

func TestToken_Pending(t *testing.T) {
	mockDB := new(MockDatabase)
	repository.DB = mockDB

	payload := model.TokenExchangePayload{
		DeviceCode: "devicecode1234567890123456789012",
		MACAddress: "00:11:22:33:44:55",
	}
	body, _ := json.Marshal(payload)

	userCode := "ABCD-1234"
	session := &model.DeviceFlowSession{
		DeviceCode: payload.DeviceCode,
		MACAddress: payload.MACAddress,
		UIDESP:     "esp-32-id",
		SessionID:  "session-abc-123",
		Status:     "authorization_pending",
	}

	mockDB.On("FindDeviceFlowByDeviceCode", mock.Anything, payload.DeviceCode).Return(userCode, session, nil)

	r := SetupDeviceRouter()
	req, _ := http.NewRequest("POST", "/api/v1/oauth/token", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "authorization_pending", resp["error"])
	mockDB.AssertExpectations(t)
}

func TestToken_Success(t *testing.T) {
	mockDB := new(MockDatabase)
	repository.DB = mockDB

	payload := model.TokenExchangePayload{
		DeviceCode: "devicecode1234567890123456789012",
		MACAddress: "00:11:22:33:44:55",
	}
	body, _ := json.Marshal(payload)

	userCode := "ABCD-1234"
	session := &model.DeviceFlowSession{
		DeviceCode: payload.DeviceCode,
		MACAddress: payload.MACAddress,
		UIDESP:     "esp-32-id",
		SessionID:  "session-abc-123",
		Status:     "approved",
		OwnerUID:   "user-123",
	}

	mockDB.On("FindDeviceFlowByDeviceCode", mock.Anything, payload.DeviceCode).Return(userCode, session, nil)
	mockDB.On("SaveDevice", mock.Anything, mock.MatchedBy(func(d *model.Device) bool {
		return d.ID == payload.MACAddress && d.OwnerUID == "user-123" && d.AccessToken != ""
	})).Return(nil)
	mockDB.On("DeleteDeviceFlow", mock.Anything, userCode).Return(nil)

	r := SetupDeviceRouter()
	req, _ := http.NewRequest("POST", "/api/v1/oauth/token", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Contains(t, resp, "access_token")
	assert.Equal(t, "bearer", resp["token_type"])
	mockDB.AssertExpectations(t)
}

func TestMqttAuth_Success(t *testing.T) {
	mockDB := new(MockDatabase)
	repository.DB = mockDB

	mac := "001122334455"
	token := "dev_token_123"
	payload := MqttAuthPayload{
		ClientID: mac,
		Username: mac,
		Password: token,
	}
	body, _ := json.Marshal(payload)

	device := &model.Device{
		ID:          mac,
		AccessToken: token,
		OwnerUID:    "user-123",
	}

	mockDB.On("GetDevice", mock.Anything, mac).Return(device, nil)

	r := SetupDeviceRouter()
	req, _ := http.NewRequest("POST", "/api/v1/mqtt/auth", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "allow", resp["result"])
}

func TestMqttAuth_Deny(t *testing.T) {
	mockDB := new(MockDatabase)
	repository.DB = mockDB

	mac := "001122334455"
	payload := MqttAuthPayload{
		ClientID: mac,
		Username: mac,
		Password: "wrong_token",
	}
	body, _ := json.Marshal(payload)

	device := &model.Device{
		ID:          mac,
		AccessToken: "correct_token",
		OwnerUID:    "user-123",
	}

	mockDB.On("GetDevice", mock.Anything, mac).Return(device, nil)

	r := SetupDeviceRouter()
	req, _ := http.NewRequest("POST", "/api/v1/mqtt/auth", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "deny", resp["result"])
}

func TestMqttAcl_Allow(t *testing.T) {
	payload := MqttAclPayload{
		ClientID: "001122334455",
		Username: "001122334455",
		Topic:    "devices/001122334455/telemetry",
		Access:   "publish",
	}
	body, _ := json.Marshal(payload)

	r := SetupDeviceRouter()
	req, _ := http.NewRequest("POST", "/api/v1/mqtt/acl", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "allow", resp["result"])
}

func TestMqttAcl_Deny(t *testing.T) {
	payload := MqttAclPayload{
		ClientID: "001122334455",
		Username: "001122334455",
		Topic:    "devices/other_device/telemetry",
		Access:   "publish",
	}
	body, _ := json.Marshal(payload)

	r := SetupDeviceRouter()
	req, _ := http.NewRequest("POST", "/api/v1/mqtt/acl", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "deny", resp["result"])
}

func TestDeviceUnpair_Success(t *testing.T) {
	mockDB := new(MockDatabase)
	repository.DB = mockDB

	claims := &Claims{
		UIDUser: "user-123",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, _ := token.SignedString(JWTSecret)

	mac := "001122334455"
	device := &model.Device{
		ID:          mac,
		OwnerUID:    "user-123",
		AccessToken: "dev_token_123",
	}

	mockDB.On("GetDevice", mock.Anything, mac).Return(device, nil)
	mockDB.On("DeleteDevice", mock.Anything, mac).Return(nil)

	r := SetupDeviceRouter()
	req, _ := http.NewRequest("DELETE", "/api/v1/devices/"+mac, nil)
	req.Header.Set("Authorization", "Bearer "+tokenString)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "Device unpaired successfully", resp["message"])
	mockDB.AssertExpectations(t)
}

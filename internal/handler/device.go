package handler

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strings"
	"time"

	"medical-iot-backend/internal/model"
	"medical-iot-backend/internal/repository"
	"medical-iot-backend/internal/worker"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

// Helper to generate a random hex string of specified length
func generateRandomString(n int) string {
	bytes := make([]byte, n/2)
	if _, err := rand.Read(bytes); err != nil {
		panic(err)
	}
	return hex.EncodeToString(bytes)
}

// Generates AAAA-1111 user code
func generateUserCode() string {
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const numbers = "0123456789"
	var sb strings.Builder

	for i := 0; i < 4; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		sb.WriteByte(letters[n.Int64()])
	}
	sb.WriteByte('-')
	for i := 0; i < 4; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(numbers))))
		sb.WriteByte(numbers[n.Int64()])
	}
	return sb.String()
}

// Compute expected HMAC-SHA256 signature for verification
func ComputePinPopSignature(userCode, macAddress, sessionId string) string {
	h := hmac.New(sha256.New, []byte("pin-pop-secret-key"))
	h.Write([]byte(userCode + ":" + macAddress + ":" + sessionId))
	return hex.EncodeToString(h.Sum(nil))
}

// UpdateRealFirebaseProvisioning ghi trạng thái Provisioning lên Firebase Realtime Database
func UpdateRealFirebaseProvisioning(ctx context.Context, mac, sessionId, userCode, pairingNonce string) error {
	if repository.Firebase != nil {
		return repository.Firebase.UpdateProvisioningStatus(ctx, mac, sessionId, userCode, pairingNonce)
	}
	log.Printf("[Firebase WARNING] Firebase Client chưa được khởi tạo. Không thể ghi nhận trạng thái pairing!")
	return fmt.Errorf("firebase client uninitialized")
}

func DeviceAuthorizeHandler(c *gin.Context) {
	var payload model.DeviceAuthorizePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	deviceCode := generateRandomString(32)
	userCode := generateUserCode()
	pairingNonce := generateRandomString(16)

	session := &model.DeviceFlowSession{
		DeviceCode:   deviceCode,
		MACAddress:   payload.MACAddress,
		UIDESP:       "esp32-" + generateRandomString(8),
		SessionID:    payload.SessionID,
		Status:       "authorization_pending",
		PairingNonce: pairingNonce,
	}

	err := repository.DB.SetDeviceFlow(c.Request.Context(), userCode, session, 300*time.Second)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save authorization session"})
		return
	}

	// Dọn dẹp bản ghi polling cũ nếu có trên Firebase trước khi tạo phiên mới
	if repository.Firebase != nil {
		_ = repository.Firebase.DeleteProvisioningStatus(c.Request.Context(), payload.MACAddress, payload.SessionID)
	}

	// Ghi nhận trạng thái thật lên Firebase phục vụ cơ chế polling phía App/Web frontend
	if err := UpdateRealFirebaseProvisioning(c.Request.Context(), payload.MACAddress, payload.SessionID, userCode, pairingNonce); err != nil {
		log.Printf("[Firebase Error] Lỗi đồng bộ luồng pairing: %v", err)
	}

	c.JSON(http.StatusOK, gin.H{
		"device_code":      deviceCode,
		"user_code":        userCode,
		"verification_uri": "http://localhost:8080/api/v1/oauth/device/confirm",
		"expires_in":       300,
		"interval":         5,
	})
}

func DeviceConfirmHandler(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization token required"})
		return
	}
	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return JWTSecret, nil
	})
	if err != nil || !token.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		return
	}

	var payload model.DeviceConfirmPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	session, err := repository.DB.GetDeviceFlow(c.Request.Context(), payload.UserCode)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Authorization session expired or not found"})
		return
	}

	if session.MACAddress != payload.MACAddress || session.SessionID != payload.SessionID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Device session mismatch"})
		return
	}

	// Validate Pin POP Signature using the dynamic PairingNonce from Redis
	h := hmac.New(sha256.New, []byte(session.PairingNonce))
	h.Write([]byte(payload.UserCode + ":" + payload.MACAddress + ":" + payload.SessionID))
	expectedSig := hex.EncodeToString(h.Sum(nil))

	if payload.PinPopSignature != expectedSig {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid PIN PoP signature"})
		return
	}

	session.Status = "approved"
	session.OwnerUID = claims.UIDUser

	err = repository.DB.SetDeviceFlow(c.Request.Context(), payload.UserCode, session, 300*time.Second)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update authorization status"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Device authorization approved successfully"})
}

func DeviceTokenHandler(c *gin.Context) {
	var payload model.TokenExchangePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	userCode, session, err := repository.DB.FindDeviceFlowByDeviceCode(c.Request.Context(), payload.DeviceCode)
	if err != nil {
		// Dọn dẹp bản ghi polling cũ trên Firebase vì phiên đã quá hạn ở Redis
		if repository.Firebase != nil {
			_ = repository.Firebase.DeleteProvisioningStatus(c.Request.Context(), payload.MACAddress, "")
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "Authorization session expired or not found"})
		return
	}

	if session.MACAddress != payload.MACAddress {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Device MAC address mismatch"})
		return
	}

	if session.Status == "authorization_pending" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "authorization_pending"})
		return
	}

	if session.Status != "approved" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_grant"})
		return
	}

	// Generate access token for the device
	accessToken := "dev_token_" + generateRandomString(32)

	device := &model.Device{
		ID:          payload.MACAddress,
		OwnerUID:    session.OwnerUID,
		AccessToken: accessToken,
		PairedAt:    time.Now(),
	}

	// Save permanently to MongoDB
	err = repository.DB.SaveDevice(c.Request.Context(), device)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to register paired device"})
		return
	}

	// Update device ownership in Firebase to enforce Security Rules
	if repository.Firebase != nil {
		if err := repository.Firebase.SetDeviceOwnership(c.Request.Context(), session.OwnerUID, payload.MACAddress); err != nil {
			log.Printf("[Firebase Error] Failed to set device ownership for security rules: %v", err)
		}
	}

	// Clean up temp Redis session
	_ = repository.DB.DeleteDeviceFlow(c.Request.Context(), userCode)

	// Clean up the Firebase polling entry now that pairing is complete. session.SessionID
	// is the real key used when the entry was created in DeviceAuthorizeHandler — unlike
	// the expired-session branch above, we actually have it here.
	if repository.Firebase != nil {
		_ = repository.Firebase.DeleteProvisioningStatus(c.Request.Context(), payload.MACAddress, session.SessionID)
	}

	c.JSON(http.StatusOK, gin.H{
		"access_token": accessToken,
		"token_type":   "bearer",
	})
}

// MqttAuthPayload defines payload from EMQX HTTP Auth request
type MqttAuthPayload struct {
	ClientID string `json:"clientid"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// MqttAuthHandler authenticates MQTT connections from devices using their access token
func MqttAuthHandler(c *gin.Context) {
	var payload MqttAuthPayload
	// Fallback to form/query binding since EMQX can send form data or JSON
	if err := c.ShouldBind(&payload); err != nil {
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"result": "deny", "error": "Invalid request payload"})
			return
		}
	}

	log.Printf("[DOCKER-DEBUG] MqttAuthHandler parsed payload: ClientID='%s', Username='%s', Password='%s'", payload.ClientID, payload.Username, payload.Password)

	mac := payload.ClientID
	token := payload.Password

	if mac == "" || token == "" {
		log.Printf("[DOCKER-DEBUG] MQTT Auth rejected: ClientID or Password empty (mac='%s', token='%s')", mac, token)
		c.JSON(http.StatusBadRequest, gin.H{"result": "deny", "error": "Missing clientid or password"})
		return
	}

	// The backend's own MQTT worker isn't a paired device, so it authenticates via a
	// shared secret instead of a per-device token lookup in MongoDB.
	if mac == repository.MQTTWorkerClientID {
		if token == repository.MQTTWorkerSecret {
			c.JSON(http.StatusOK, gin.H{"result": "allow"})
		} else {
			c.JSON(http.StatusForbidden, gin.H{"result": "deny", "error": "Invalid worker credentials"})
		}
		return
	}

	// Lookup device in DB to check token
	device, err := repository.DB.GetDevice(c.Request.Context(), mac)
	if err != nil || device == nil {
		c.JSON(http.StatusForbidden, gin.H{"result": "deny", "error": "Device not registered"})
		return
	}

	if device.AccessToken != token {
		c.JSON(http.StatusForbidden, gin.H{"result": "deny", "error": "Invalid access token"})
		return
	}

	// Access granted
	c.JSON(http.StatusOK, gin.H{"result": "allow"})
}

// MqttAclPayload defines payload from EMQX HTTP ACL request
type MqttAclPayload struct {
	ClientID string `json:"clientid"`
	Username string `json:"username"`
	Topic    string `json:"topic"`
	Access   string `json:"action"` // "publish" or "subscribe"
}

// MqttAclHandler enforces authorization rules for topic access on EMQX
func MqttAclHandler(c *gin.Context) {
	var payload MqttAclPayload
	if err := c.ShouldBind(&payload); err != nil {
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"result": "deny", "error": "Invalid request payload"})
			return
		}
	}

	// Rule: the backend's own MQTT worker may subscribe to the telemetry and status wildcard topics
	if payload.ClientID == repository.MQTTWorkerClientID && payload.Access == "subscribe" &&
		(payload.Topic == "devices/+/telemetry" || payload.Topic == "devices/+/status") {
		c.JSON(http.StatusOK, gin.H{"result": "allow"})
		return
	}

	// Rule: the backend's own MQTT worker may publish to any device's command topic
	if payload.ClientID == repository.MQTTWorkerClientID && payload.Access == "publish" &&
		strings.HasPrefix(payload.Topic, "devices/") && strings.HasSuffix(payload.Topic, "/command") {
		c.JSON(http.StatusOK, gin.H{"result": "allow"})
		return
	}

	// Rule: Device can only publish to its own "devices/{clientid}/telemetry" topic and its
	// own "devices/{clientid}/status" topic (the online heartbeat published right after
	// connect, and the LWT payload EMQX auto-publishes on behalf of the device when it drops).
	if payload.Access == "publish" &&
		(payload.Topic == fmt.Sprintf("devices/%s/telemetry", payload.ClientID) ||
			payload.Topic == fmt.Sprintf("devices/%s/status", payload.ClientID)) {
		c.JSON(http.StatusOK, gin.H{"result": "allow"})
		return
	}

	// Rule: Device may only subscribe to its own "devices/{clientid}/command" topic
	if payload.Access == "subscribe" && payload.Topic == fmt.Sprintf("devices/%s/command", payload.ClientID) {
		c.JSON(http.StatusOK, gin.H{"result": "allow"})
		return
	}

	// Deny by default for any other requests
	c.JSON(http.StatusForbidden, gin.H{"result": "deny"})
}

// DeviceUnpairHandler deletes the paired device mapping from MongoDB and Firebase (Unpair)
func DeviceUnpairHandler(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization token required"})
		return
	}
	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return JWTSecret, nil
	})
	if err != nil || !token.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		return
	}

	mac := c.Param("mac")
	if mac == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MAC address parameter is required"})
		return
	}

	// 1. Check ownership in MongoDB
	device, err := repository.DB.GetDevice(c.Request.Context(), mac)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database lookup failed"})
		return
	}
	if device == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Device not found"})
		return
	}
	if device.OwnerUID != claims.UIDUser {
		c.JSON(http.StatusForbidden, gin.H{"error": "You do not own this device"})
		return
	}

	// 2. Delete device from MongoDB
	err = repository.DB.DeleteDevice(c.Request.Context(), mac)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to unpair device"})
		return
	}

	// 3. Delete mapping from Firebase to enforce Security Rules (block clients)
	if repository.Firebase != nil {
		_ = repository.Firebase.DeleteDeviceOwnership(c.Request.Context(), mac)
	}

	// 4. Kick the device's live MQTT session (if any) so the revoked access token takes
	// effect immediately, instead of the device only noticing the next time its session
	// happens to drop on its own and it tries to reconnect. The device's MQTT ClientID is
	// its own MAC address (see ESP32 firmware's mqttClient.connect(macAddress, ...)).
	if repository.EMQX != nil {
		if err := repository.EMQX.KickClient(c.Request.Context(), mac); err != nil {
			log.Printf("[Unpair] Failed to kick MQTT session for device %s: %v", mac, err)
		}
	}

	c.JSON(http.StatusOK, gin.H{"message": "Device unpaired successfully"})
}

// DeviceRequestReconfigHandler lets the owner remotely ask an already-paired device to reopen
// BLE provisioning (e.g. Wi-Fi needs to change but nobody is physically near the device).
func DeviceRequestReconfigHandler(c *gin.Context) {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization token required"})
		return
	}
	tokenStr := strings.TrimPrefix(authHeader, "Bearer ")

	claims := &Claims{}
	token, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return JWTSecret, nil
	})
	if err != nil || !token.Valid {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid token"})
		return
	}

	mac := c.Param("mac")
	if mac == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "MAC address parameter is required"})
		return
	}

	device, err := repository.DB.GetDevice(c.Request.Context(), mac)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Database lookup failed"})
		return
	}
	if device == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Device not found"})
		return
	}
	if device.OwnerUID != claims.UIDUser {
		c.JSON(http.StatusForbidden, gin.H{"error": "You do not own this device"})
		return
	}

	if err := worker.PublishCommand(mac, `{"cmd":"open_provisioning"}`); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Device is not currently reachable: " + err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Reconfiguration request sent to device"})
}

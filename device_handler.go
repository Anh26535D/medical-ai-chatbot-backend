package main

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

// Cập nhật trạng thái Provisioning thật lên Firebase Realtime Database qua REST API Client
func UpdateRealFirebaseProvisioning(ctx context.Context, mac, sessionId, userCode, deviceCode string) error {
	if Firebase != nil {
		return Firebase.UpdateProvisioningStatus(ctx, mac, sessionId, userCode, deviceCode)
	}
	log.Printf("[Firebase WARNING] Firebase Client chưa được khởi tạo. Không thể ghi nhận trạng thái pairing!")
	return fmt.Errorf("firebase client uninitialized")
}

func DeviceAuthorizeHandler(c *gin.Context) {
	var payload DeviceAuthorizePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	deviceCode := generateRandomString(32)
	userCode := generateUserCode()

	session := &DeviceFlowSession{
		DeviceCode: deviceCode,
		MACAddress: payload.MACAddress,
		UIDESP:     "esp32-" + generateRandomString(8),
		SessionID:  payload.SessionID,
		Status:     "authorization_pending",
	}

	err := DB.SetDeviceFlow(c.Request.Context(), userCode, session, 300*time.Second)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save authorization session"})
		return
	}

	// Ghi nhận trạng thái thật lên Firebase phục vụ cơ chế polling phía App/Web frontend
	if err := UpdateRealFirebaseProvisioning(c.Request.Context(), payload.MACAddress, payload.SessionID, userCode, deviceCode); err != nil {
		log.Printf("[Firebase Error] Lỗi đồng bộ luồng pairing: %v", err)
		// Không ngắt luồng HTTP nếu lưu database chính (Redis/Mongo) đã thành công
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

	var payload DeviceConfirmPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	// Validate Pin POP Signature
	expectedSig := ComputePinPopSignature(payload.UserCode, payload.MACAddress, payload.SessionID)
	if payload.PinPopSignature != expectedSig {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid PIN PoP signature"})
		return
	}

	session, err := DB.GetDeviceFlow(c.Request.Context(), payload.UserCode)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Authorization session expired or not found"})
		return
	}

	if session.MACAddress != payload.MACAddress || session.SessionID != payload.SessionID {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Device session mismatch"})
		return
	}

	session.Status = "approved"
	session.OwnerUID = claims.UIDUser

	err = DB.SetDeviceFlow(c.Request.Context(), payload.UserCode, session, 300*time.Second)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to update authorization status"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Device authorization approved successfully"})
}

func DeviceTokenHandler(c *gin.Context) {
	var payload TokenExchangePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request payload"})
		return
	}

	userCode, session, err := DB.FindDeviceFlowByDeviceCode(c.Request.Context(), payload.DeviceCode)
	if err != nil {
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

	device := &Device{
		ID:          payload.MACAddress,
		OwnerUID:    session.OwnerUID,
		AccessToken: accessToken,
		PairedAt:    time.Now(),
	}

	// Save permanently to MongoDB
	err = DB.SaveDevice(c.Request.Context(), device)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to register paired device"})
		return
	}

	// Clean up temp Redis session
	_ = DB.DeleteDeviceFlow(c.Request.Context(), userCode)

	c.JSON(http.StatusOK, gin.H{
		"access_token": accessToken,
		"token_type":   "bearer",
	})
}

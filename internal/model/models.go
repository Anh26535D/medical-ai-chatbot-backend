package model

import (
	"time"
)

// User represents the user document in MongoDB
type User struct {
	ID           string    `bson:"_id,omitempty" json:"id"`
	Phone        string    `bson:"phone" json:"phone"`
	PasswordHash string    `bson:"password_hash" json:"-"`
	CreatedAt    time.Time `bson:"created_at" json:"created_at"`
}

// Device represents the paired device in MongoDB
type Device struct {
	ID          string    `bson:"_id" json:"mac_address"` // MAC Address is the key _id
	OwnerUID    string    `bson:"owner_uid" json:"owner_uid"`
	AccessToken string    `bson:"access_token" json:"access_token"`
	PairedAt    time.Time `bson:"paired_at" json:"paired_at"`
}

// TelemetryDataPoint represents a single health reading
type TelemetryDataPoint struct {
	Timestamp   int64   `bson:"t" json:"t"`
	BPM         int     `bson:"bpm" json:"bpm"`
	SPO2        int     `bson:"spo2" json:"spo2"`
	Temperature float64 `bson:"temp" json:"temp"`
	Humidity    float64 `bson:"hum" json:"hum"`
	Status      string  `bson:"status" json:"status"`
}

// TelemetryHistory represents hourly bucket pattern in MongoDB
type TelemetryHistory struct {
	ID         string               `bson:"_id" json:"id"` // "{mac}_{date}_{hour}"
	MACAddress string               `bson:"mac_address" json:"mac_address"`
	Date       string               `bson:"date" json:"date"` // YYYY-MM-DD
	Hour       int                  `bson:"hour" json:"hour"` // 0-23
	DataPoints []TelemetryDataPoint `bson:"data_points" json:"data_points"`
}

// DeviceFlowSession represents data cached in Redis for RFC 8628 Device Flow
type DeviceFlowSession struct {
	DeviceCode string `json:"device_code"`
	MACAddress string `json:"mac_address"`
	UIDESP     string `json:"uid_esp"`
	SessionID  string `json:"session_id"`
	Status     string `json:"status"` // "authorization_pending" or "approved"
	OwnerUID   string `json:"owner_uid,omitempty"`
}

// RegisterPayload defines the body for registration
type RegisterPayload struct {
	Phone    string `json:"phone" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginPayload defines the body for login
type LoginPayload struct {
	Phone    string `json:"phone" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// DeviceAuthorizePayload defines client parameters for /api/v1/oauth/device/authorize
type DeviceAuthorizePayload struct {
	MACAddress string `json:"mac_address" binding:"required"`
	SessionID  string `json:"session_id" binding:"required"`
}

// DeviceConfirmPayload defines payload from User app to confirm the device mapping
type DeviceConfirmPayload struct {
	UserCode        string `json:"user_code" binding:"required"`
	MACAddress      string `json:"mac_address" binding:"required"`
	SessionID       string `json:"session_id" binding:"required"`
	PinPopSignature string `json:"pin_pop_signature" binding:"required"`
}

// TokenExchangePayload defines polling request parameters from ESP32 to /api/v1/oauth/token
type TokenExchangePayload struct {
	DeviceCode string `json:"device_code" binding:"required"`
	MACAddress string `json:"mac_address" binding:"required"`
}

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"medical-iot-backend/internal/model"
	"medical-iot-backend/internal/repository"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"golang.org/x/crypto/bcrypt"
)

// MockDatabase is a mock implementation of the DatabaseService interface
type MockDatabase struct {
	mock.Mock
}

func (m *MockDatabase) FindUserByPhone(ctx context.Context, phone string) (*model.User, error) {
	args := m.Called(ctx, phone)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*model.User), args.Error(1)
}

func (m *MockDatabase) CreateUser(ctx context.Context, user *model.User) error {
	args := m.Called(ctx, user)
	return args.Error(0)
}

func (m *MockDatabase) SaveDevice(ctx context.Context, device *model.Device) error {
	args := m.Called(ctx, device)
	return args.Error(0)
}

func (m *MockDatabase) GetDevice(ctx context.Context, mac string) (*model.Device, error) {
	args := m.Called(ctx, mac)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*model.Device), args.Error(1)
}

func (m *MockDatabase) DeleteDevice(ctx context.Context, mac string) error {
	args := m.Called(ctx, mac)
	return args.Error(0)
}

func (m *MockDatabase) UpdateTelemetryHistory(ctx context.Context, mac string, date string, hour int, point model.TelemetryDataPoint) error {
	args := m.Called(ctx, mac, date, hour, point)
	return args.Error(0)
}

func (m *MockDatabase) SetDeviceFlow(ctx context.Context, userCode string, session *model.DeviceFlowSession, ttl time.Duration) error {
	args := m.Called(ctx, userCode, session, ttl)
	return args.Error(0)
}

func (m *MockDatabase) GetDeviceFlow(ctx context.Context, userCode string) (*model.DeviceFlowSession, error) {
	args := m.Called(ctx, userCode)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*model.DeviceFlowSession), args.Error(1)
}

func (m *MockDatabase) DeleteDeviceFlow(ctx context.Context, userCode string) error {
	args := m.Called(ctx, userCode)
	return args.Error(0)
}

func (m *MockDatabase) FindDeviceFlowByDeviceCode(ctx context.Context, deviceCode string) (string, *model.DeviceFlowSession, error) {
	args := m.Called(ctx, deviceCode)
	if args.Get(1) == nil {
		return args.String(0), nil, args.Error(2)
	}
	return args.String(0), args.Get(1).(*model.DeviceFlowSession), args.Error(2)
}

func SetupAuthRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/auth/register", RegisterHandler)
	r.POST("/api/v1/auth/login", LoginHandler)
	return r
}

func TestRegister_Success(t *testing.T) {
	mockDB := new(MockDatabase)
	repository.DB = mockDB

	payload := model.RegisterPayload{
		Phone:    "0987654321",
		Password: "SecurePassword123",
	}
	body, _ := json.Marshal(payload)

	mockDB.On("FindUserByPhone", mock.Anything, payload.Phone).Return((*model.User)(nil), nil)
	mockDB.On("CreateUser", mock.Anything, mock.MatchedBy(func(u *model.User) bool {
		return u.Phone == payload.Phone && bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(payload.Password)) == nil
	})).Return(nil)

	r := SetupAuthRouter()
	req, _ := http.NewRequest("POST", "/api/v1/auth/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "Registration successful", resp["message"])
	mockDB.AssertExpectations(t)
}

func TestRegister_DuplicatePhone(t *testing.T) {
	mockDB := new(MockDatabase)
	repository.DB = mockDB

	payload := model.RegisterPayload{
		Phone:    "0987654321",
		Password: "SecurePassword123",
	}
	body, _ := json.Marshal(payload)

	existingUser := &model.User{
		ID:           "user-123",
		Phone:        payload.Phone,
		PasswordHash: "somehash",
		CreatedAt:    time.Now(),
	}
	mockDB.On("FindUserByPhone", mock.Anything, payload.Phone).Return(existingUser, nil)

	r := SetupAuthRouter()
	req, _ := http.NewRequest("POST", "/api/v1/auth/register", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "Phone number already registered", resp["error"])
	mockDB.AssertExpectations(t)
}

func TestLogin_Success(t *testing.T) {
	mockDB := new(MockDatabase)
	repository.DB = mockDB

	password := "SecurePassword123"
	hashedPassword, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)

	payload := model.LoginPayload{
		Phone:    "0987654321",
		Password: password,
	}
	body, _ := json.Marshal(payload)

	user := &model.User{
		ID:           "user-123",
		Phone:        payload.Phone,
		PasswordHash: string(hashedPassword),
		CreatedAt:    time.Now(),
	}

	mockDB.On("FindUserByPhone", mock.Anything, payload.Phone).Return(user, nil)

	r := SetupAuthRouter()
	req, _ := http.NewRequest("POST", "/api/v1/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Contains(t, resp, "token")
	assert.NotEmpty(t, resp["token"])
	mockDB.AssertExpectations(t)
}

func TestLogin_WrongPassword(t *testing.T) {
	mockDB := new(MockDatabase)
	repository.DB = mockDB

	hashedPassword, _ := bcrypt.GenerateFromPassword([]byte("CorrectPassword"), bcrypt.DefaultCost)

	payload := model.LoginPayload{
		Phone:    "0987654321",
		Password: "WrongPassword",
	}
	body, _ := json.Marshal(payload)

	user := &model.User{
		ID:           "user-123",
		Phone:        payload.Phone,
		PasswordHash: string(hashedPassword),
		CreatedAt:    time.Now(),
	}

	mockDB.On("FindUserByPhone", mock.Anything, payload.Phone).Return(user, nil)

	r := SetupAuthRouter()
	req, _ := http.NewRequest("POST", "/api/v1/auth/login", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	assert.Equal(t, "Invalid phone or password", resp["error"])
	mockDB.AssertExpectations(t)
}

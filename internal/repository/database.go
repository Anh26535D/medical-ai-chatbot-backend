package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"medical-iot-backend/internal/model"

	"github.com/go-redis/redis/v8"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DatabaseService defines all database operations needed by the application.
type DatabaseService interface {
	FindUserByPhone(ctx context.Context, phone string) (*model.User, error)
	CreateUser(ctx context.Context, user *model.User) error
	SaveDevice(ctx context.Context, device *model.Device) error
	GetDevice(ctx context.Context, mac string) (*model.Device, error)
	DeleteDevice(ctx context.Context, mac string) error
	UpdateTelemetryHistory(ctx context.Context, mac string, date string, hour int, point model.TelemetryDataPoint) error

	SetDeviceFlow(ctx context.Context, userCode string, session *model.DeviceFlowSession, ttl time.Duration) error
	GetDeviceFlow(ctx context.Context, userCode string) (*model.DeviceFlowSession, error)
	DeleteDeviceFlow(ctx context.Context, userCode string) error
	FindDeviceFlowByDeviceCode(ctx context.Context, deviceCode string) (string, *model.DeviceFlowSession, error)
}

// Global DB instance
var DB DatabaseService

// RealDatabase implements DatabaseService using MongoDB and Redis
type RealDatabase struct {
	MongoClient *mongo.Client
	RedisClient *redis.Client
	MongoDbName string
}

func (r *RealDatabase) FindUserByPhone(ctx context.Context, phone string) (*model.User, error) {
	collection := r.MongoClient.Database(r.MongoDbName).Collection("users")
	var user model.User
	err := collection.FindOne(ctx, bson.M{"phone": phone}).Decode(&user)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	return &user, err
}

func (r *RealDatabase) CreateUser(ctx context.Context, user *model.User) error {
	collection := r.MongoClient.Database(r.MongoDbName).Collection("users")
	_, err := collection.InsertOne(ctx, user)
	return err
}

func (r *RealDatabase) SaveDevice(ctx context.Context, device *model.Device) error {
	collection := r.MongoClient.Database(r.MongoDbName).Collection("devices")
	opts := options.Replace().SetUpsert(true)
	_, err := collection.ReplaceOne(ctx, bson.M{"_id": device.ID}, device, opts)
	return err
}

func (r *RealDatabase) GetDevice(ctx context.Context, mac string) (*model.Device, error) {
	collection := r.MongoClient.Database(r.MongoDbName).Collection("devices")
	var device model.Device
	err := collection.FindOne(ctx, bson.M{"_id": mac}).Decode(&device)
	if err == mongo.ErrNoDocuments {
		return nil, nil
	}
	return &device, err
}

func (r *RealDatabase) DeleteDevice(ctx context.Context, mac string) error {
	collection := r.MongoClient.Database(r.MongoDbName).Collection("devices")
	_, err := collection.DeleteOne(ctx, bson.M{"_id": mac})
	return err
}

func (r *RealDatabase) UpdateTelemetryHistory(ctx context.Context, mac string, date string, hour int, point model.TelemetryDataPoint) error {
	collection := r.MongoClient.Database(r.MongoDbName).Collection("telemetry_history")
	id := fmt.Sprintf("%s_%s_%02d", mac, date, hour)

	filter := bson.M{"_id": id}
	update := bson.M{
		"$setOnInsert": bson.M{
			"mac_address": mac,
			"date":        date,
			"hour":        hour,
		},
		"$push": bson.M{
			"data_points": point,
		},
	}
	opts := options.Update().SetUpsert(true)
	_, err := collection.UpdateOne(ctx, filter, update, opts)
	return err
}

func (r *RealDatabase) SetDeviceFlow(ctx context.Context, userCode string, session *model.DeviceFlowSession, ttl time.Duration) error {
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	key := "device_flow:" + userCode
	return r.RedisClient.Set(ctx, key, data, ttl).Err()
}

func (r *RealDatabase) GetDeviceFlow(ctx context.Context, userCode string) (*model.DeviceFlowSession, error) {
	key := "device_flow:" + userCode
	val, err := r.RedisClient.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, errors.New("device flow session not found")
	} else if err != nil {
		return nil, err
	}
	var session model.DeviceFlowSession
	err = json.Unmarshal([]byte(val), &session)
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (r *RealDatabase) DeleteDeviceFlow(ctx context.Context, userCode string) error {
	key := "device_flow:" + userCode
	return r.RedisClient.Del(ctx, key).Err()
}

func (r *RealDatabase) FindDeviceFlowByDeviceCode(ctx context.Context, deviceCode string) (string, *model.DeviceFlowSession, error) {
	var cursor uint64
	for {
		keys, nextCursor, err := r.RedisClient.Scan(ctx, cursor, "device_flow:*", 10).Result()
		if err != nil {
			return "", nil, err
		}
		for _, key := range keys {
			val, err := r.RedisClient.Get(ctx, key).Result()
			if err != nil {
				continue
			}
			var session model.DeviceFlowSession
			if err := json.Unmarshal([]byte(val), &session); err == nil {
				if session.DeviceCode == deviceCode {
					userCode := key[12:]
					return userCode, &session, nil
				}
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return "", nil, errors.New("device flow session not found")
}

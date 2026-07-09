package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	log.Println("Starting Medical IoT Backend...")

	// 1. Initialize MongoDB Connection
	mongoURI := os.Getenv("MONGODB_URI")
	if mongoURI == "" {
		mongoURI = "mongodb://localhost:27017"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		log.Fatalf("Failed to connect to MongoDB: %v", err)
	}

	// Verify MongoDB connection
	err = mongoClient.Ping(ctx, nil)
	if err != nil {
		log.Printf("Warning: MongoDB connection is currently unreachable: %v", err)
	} else {
		log.Println("Connected to MongoDB successfully")
	}

	// 2. Initialize Redis Connection
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}
	redisClient := redis.NewClient(&redis.Options{
		Addr: redisAddr,
	})

	// Verify Redis connection
	redisCtx, redisCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer redisCancel()
	err = redisClient.Ping(redisCtx).Err()
	if err != nil {
		log.Printf("Warning: Redis connection is currently unreachable: %v", err)
	} else {
		log.Println("Connected to Redis successfully")
	}

	// Set global Database instance
	DB = &RealDatabase{
		MongoClient: mongoClient,
		RedisClient: redisClient,
		MongoDbName: "medical_iot_db",
	}

	// 3. Start MQTT worker in background
	mqttBroker := os.Getenv("MQTT_BROKER_URI")
	if mqttBroker == "" {
		mqttBroker = "tcp://localhost:1883"
	}
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()

	log.Printf("Starting MQTT Worker subscribing to %s...", mqttBroker)
	StartMQTTWorker(workerCtx, mqttBroker)

	// 4. Configure HTTP Gin Engine
	ginMode := os.Getenv("GIN_MODE")
	if ginMode != "" {
		gin.SetMode(ginMode)
	}
	r := gin.Default()

	// Healthcheck route
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "UP"})
	})

	// API Routes
	v1 := r.Group("/api/v1")
	{
		// Authentication Routes
		auth := v1.Group("/auth")
		{
			auth.POST("/register", RegisterHandler)
			auth.POST("/login", LoginHandler)
		}

		// Device Flow (RFC 8628) Routes
		oauth := v1.Group("/oauth")
		{
			oauth.POST("/device/authorize", DeviceAuthorizeHandler)
			oauth.POST("/device/confirm", DeviceConfirmHandler)
			oauth.POST("/token", DeviceTokenHandler)
		}
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Starting server on port %s...", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("Server failed to run: %v", err)
	}
}

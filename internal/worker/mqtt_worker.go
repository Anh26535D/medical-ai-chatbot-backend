package worker

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"medical-iot-backend/internal/model"
	"medical-iot-backend/internal/repository"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Evaluate clinical status based on BPM, SPO2, and Temperature thresholds
func evaluateStatus(bpm, spo2 int, temp float64) string {
	// Normal resting heart rate: 60-100 bpm. Normal blood oxygen level: 95-100%. Temp warning > 39.0°C.
	if bpm < 60 || bpm > 100 || spo2 < 95 || temp > 39.0 {
		return "Warning"
	}
	return "Normal"
}

func StartMQTTWorker(ctx context.Context, brokerURI string) {
	opts := mqtt.NewClientOptions().AddBroker(brokerURI)
	opts.SetClientID("medical_iot_backend_worker")
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)

	opts.OnConnect = func(client mqtt.Client) {
		log.Println("[MQTT Worker] Connected to EMQX Broker successfully")
		token := client.Subscribe("devices/+/telemetry", 1, func(client mqtt.Client, msg mqtt.Message) {
			go handleTelemetryMessage(ctx, msg)
		})
		if token.Wait() && token.Error() != nil {
			log.Printf("[MQTT Worker] Subscription error: %v", token.Error())
		} else {
			log.Println("[MQTT Worker] Subscribed to topic: devices/+/telemetry")
		}
	}

	opts.OnConnectionLost = func(client mqtt.Client, err error) {
		log.Printf("[MQTT Worker] Connection lost: %v", err)
	}

	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		log.Printf("[MQTT Worker] Broker connection failed: %v. Retrying in background...", token.Error())
	}

	// Keep alive or clean up on context done
	go func() {
		<-ctx.Done()
		log.Println("[MQTT Worker] Context cancelled, disconnecting client...")
		client.Disconnect(250)
	}()
}

func handleTelemetryMessage(ctx context.Context, msg mqtt.Message) {
	topic := msg.Topic()
	payload := string(msg.Payload())

	log.Printf("[MQTT Worker] Received message. Topic: %s, Payload: %s", topic, payload)

	// Topic pattern: devices/{mac}/telemetry
	parts := strings.Split(topic, "/")
	if len(parts) < 3 {
		log.Printf("[MQTT Worker] Invalid topic format: %s", topic)
		return
	}
	mac := parts[1]

	// Payload format: "bpm,spo2,temp,hum" (e.g., "75,98,32.5,65.0")
	dataParts := strings.Split(payload, ",")
	if len(dataParts) != 4 {
		log.Printf("[MQTT Worker] Invalid payload format (expected bpm,spo2,temp,hum): %s", payload)
		return
	}

	bpm, err1 := strconv.Atoi(strings.TrimSpace(dataParts[0]))
	spo2, err2 := strconv.Atoi(strings.TrimSpace(dataParts[1]))
	temp, err3 := strconv.ParseFloat(strings.TrimSpace(dataParts[2]), 64)
	hum, err4 := strconv.ParseFloat(strings.TrimSpace(dataParts[3]), 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		log.Printf("[MQTT Worker] Failed to parse values from payload: %s", payload)
		return
	}

	status := evaluateStatus(bpm, spo2, temp)
	now := time.Now()

	point := model.TelemetryDataPoint{
		Timestamp:   now.Unix(),
		BPM:         bpm,
		SPO2:        spo2,
		Temperature: temp,
		Humidity:    hum,
		Status:      status,
	}

	date := now.Format("2006-01-02")
	hour := now.Hour()

	// Update in MongoDB hourly bucket
	err := repository.DB.UpdateTelemetryHistory(ctx, mac, date, hour, point)
	if err != nil {
		log.Printf("[MQTT Worker] Failed to update telemetry bucket in MongoDB for device %s: %v", mac, err)
	} else {
		log.Printf("[MQTT Worker] Saved telemetry data to MongoDB bucket for device %s", mac)
	}

	// Update in Firebase Realtime Database
	if repository.Firebase != nil {
		if err := repository.Firebase.UpdateLiveTelemetry(ctx, mac, point); err != nil {
			log.Printf("[MQTT Worker] Failed to update live telemetry in Firebase for device %s: %v", mac, err)
		} else {
			log.Printf("[MQTT Worker] Updated live telemetry in Firebase for device %s", mac)
		}
	}
}

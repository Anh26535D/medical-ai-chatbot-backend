package main

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Fake Firebase RTDB live updates
func FakeFirebaseLiveUpdate(mac string, bpm, spo2 int, status string) error {
	log.Printf("[Firebase Simulation] Writing to RTDB /live_devices/%s/current: bpm=%d, spo2=%d, status=%s",
		mac, bpm, spo2, status)
	return nil
}

// Evaluate clinical status based on BPM and SPO2 thresholds
func evaluateStatus(bpm, spo2 int) string {
	// Normal resting heart rate: 60-100 bpm. Normal blood oxygen level: 95-100%.
	if bpm < 60 || bpm > 100 || spo2 < 95 {
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

	// Payload format: "bpm,spo2" (e.g., "75,98")
	dataParts := strings.Split(payload, ",")
	if len(dataParts) != 2 {
		log.Printf("[MQTT Worker] Invalid payload format (expected bpm,spo2): %s", payload)
		return
	}

	bpm, err1 := strconv.Atoi(strings.TrimSpace(dataParts[0]))
	spo2, err2 := strconv.Atoi(strings.TrimSpace(dataParts[1]))
	if err1 != nil || err2 != nil {
		log.Printf("[MQTT Worker] Failed to parse integers from payload: %s", payload)
		return
	}

	status := evaluateStatus(bpm, spo2)
	now := time.Now()

	point := TelemetryDataPoint{
		Timestamp: now.Unix(),
		BPM:       bpm,
		SPO2:      spo2,
		Status:    status,
	}

	date := now.Format("2006-01-02")
	hour := now.Hour()

	// Update in MongoDB hourly bucket
	err := DB.UpdateTelemetryHistory(ctx, mac, date, hour, point)
	if err != nil {
		log.Printf("[MQTT Worker] Failed to update telemetry bucket in MongoDB for device %s: %v", mac, err)
	} else {
		log.Printf("[MQTT Worker] Saved telemetry data to MongoDB bucket for device %s", mac)
	}

	// Fake Firebase update
	_ = FakeFirebaseLiveUpdate(mac, bpm, spo2, status)
}

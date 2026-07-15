package worker

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"medical-iot-backend/internal/handler"
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
	opts.SetClientID(handler.MQTTWorkerClientID)
	// EMQX's HTTP auth/ACL webhooks authenticate every client, including this one, so the
	// worker must present the shared secret that MqttAuthHandler expects for its client ID.
	opts.SetUsername(handler.MQTTWorkerClientID)
	opts.SetPassword(handler.MQTTWorkerSecret)
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)
	// The broker's auth webhook calls back into this same process's HTTP server, which
	// hasn't started listening yet when this first connect attempt fires (it happens
	// before r.Run in main.go), so the initial attempt is expected to fail. ConnectRetry
	// makes the client keep retrying in the background until the HTTP server is up.
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(5 * time.Second)

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
	// Don't block on token.Wait() here: with ConnectRetry, the connect goroutine loops
	// internally until success and never completes the token on failure, which would
	// deadlock main() before it ever starts the HTTP server this worker depends on.
	client.Connect()

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

	// Update in MongoDB hourly bucket (Always save every raw point for clinical accuracy in historical records)
	err := repository.DB.UpdateTelemetryHistory(ctx, mac, date, hour, point)
	if err != nil {
		log.Printf("[MQTT Worker] Failed to update telemetry bucket in MongoDB for device %s: %v", mac, err)
	} else {
		log.Printf("[MQTT Worker] Saved telemetry data to MongoDB bucket for device %s", mac)
	}

	// Update in Firebase Realtime Database under users/{ownerUID}/devices/{mac}/telemetry/latest
	if repository.Firebase != nil {
		// Look up ownerUID from MongoDB
		device, err := repository.DB.GetDevice(ctx, mac)
		if err != nil || device == nil {
			log.Printf("[MQTT Worker] Device %s not found in MongoDB, skipping Firebase update", mac)
		} else {
			// Throttle and threshold logic for Firebase writes to optimize network and backend resources
			shouldUpdateFirebase := false
			
			lastSentMutex.Lock()
			lastSent, exists := lastSentTelemetry[mac]
			if !exists {
				shouldUpdateFirebase = true
			} else {
				// 1. Time-based throttle check: force update if >= 3 seconds have passed
				timeDiff := now.Sub(lastSent.Time)
				if timeDiff >= 3*time.Second {
					shouldUpdateFirebase = true
				} else {
					// 2. Threshold-based check: update if heart rate changed >= 2, temp >= 0.1, or SpO2 changed
					bpmDiff := bpm - lastSent.BPM
					if bpmDiff < 0 {
						bpmDiff = -bpmDiff
					}
					tempDiff := temp - lastSent.Temp
					if tempDiff < 0 {
						tempDiff = -tempDiff
					}
					
					if bpmDiff >= 2 || spo2 != lastSent.SPO2 || tempDiff >= 0.1 {
						shouldUpdateFirebase = true
					}
				}
			}
			
			if shouldUpdateFirebase {
				lastSentTelemetry[mac] = LastSentCache{
					Time: now,
					BPM:  bpm,
					SPO2: spo2,
					Temp: temp,
				}
				lastSentMutex.Unlock()
				
				if err := repository.Firebase.UpdateLiveTelemetry(ctx, device.OwnerUID, mac, point); err != nil {
					log.Printf("[MQTT Worker] Failed to update live telemetry in Firebase for device %s: %v", mac, err)
				} else {
					log.Printf("[MQTT Worker] Updated live telemetry in Firebase at users/%s/devices/%s/telemetry/latest", device.OwnerUID, mac)
				}
			} else {
				lastSentMutex.Unlock()
				log.Printf("[MQTT Worker] Skipped Firebase write for device %s due to Throttling/Threshold limits", mac)
			}
		}
	}
}

// In-memory cache for throttling
type LastSentCache struct {
	Time time.Time
	BPM  int
	SPO2 int
	Temp float64
}

var (
	lastSentTelemetry = make(map[string]LastSentCache)
	lastSentMutex     sync.Mutex
)


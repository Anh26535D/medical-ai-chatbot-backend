package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"medical-iot-backend/internal/model"
	"medical-iot-backend/internal/repository"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Client is the worker's own MQTT connection, exposed so HTTP handlers can publish commands via PublishCommand.
var Client mqtt.Client

// Evaluate clinical status based on BPM, SPO2, and Temperature thresholds
func evaluateStatus(bpm, spo2 int, temp float64) string {
	// Normal resting heart rate: 60-100 bpm. Normal blood oxygen level: 95-100%. Temp warning > 39.0°C.
	if bpm < 60 || bpm > 100 || spo2 < 95 || temp > 39.0 {
		return "Warning"
	}
	return "Normal"
}

// Rolling average window for BPM, keyed by device MAC. The firmware sends every raw
// per-beat BPM reading unfiltered (demo mode); smoothing now happens here instead of
// on-device, matching bpmWindowSize to the firmware's old 4-sample rolling window.
const bpmWindowSize = 4

var (
	bpmWindows      = make(map[string][]int)
	bpmWindowsMutex sync.Mutex
)

// rollingAverageBPM appends bpm to mac's window (evicting the oldest sample once the
// window is full) and returns the average of the samples currently held.
func rollingAverageBPM(mac string, bpm int) int {
	bpmWindowsMutex.Lock()
	defer bpmWindowsMutex.Unlock()

	window := append(bpmWindows[mac], bpm)
	if len(window) > bpmWindowSize {
		window = window[len(window)-bpmWindowSize:]
	}
	bpmWindows[mac] = window

	sum := 0
	for _, v := range window {
		sum += v
	}
	return sum / len(window)
}

func StartMQTTWorker(ctx context.Context, brokerURI string) {
	opts := mqtt.NewClientOptions().AddBroker(brokerURI)
	opts.SetClientID(repository.MQTTWorkerClientID)
	opts.SetUsername(repository.MQTTWorkerClientID)
	opts.SetPassword(repository.MQTTWorkerSecret)
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)
	// The auth webhook is served by this same process, which hasn't started listening yet on
	// the first connect attempt - ConnectRetry keeps retrying until the HTTP server is up.
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(5 * time.Second)

	opts.OnConnect = func(client mqtt.Client) {
		log.Println("[MQTT Worker] Connected to EMQX Broker successfully")
		
		// Subscribe to telemetry
		token1 := client.Subscribe("devices/+/telemetry", 1, func(client mqtt.Client, msg mqtt.Message) {
			go handleTelemetryMessage(ctx, msg)
		})
		if token1.Wait() && token1.Error() != nil {
			log.Printf("[MQTT Worker] Telemetry subscription error: %v", token1.Error())
		} else {
			log.Println("[MQTT Worker] Subscribed to topic: devices/+/telemetry")
		}

		// Subscribe to device online/offline status notifications (Last Will & Heartbeats)
		token2 := client.Subscribe("devices/+/status", 1, func(client mqtt.Client, msg mqtt.Message) {
			go handleStatusMessage(ctx, msg)
		})
		if token2.Wait() && token2.Error() != nil {
			log.Printf("[MQTT Worker] Status subscription error: %v", token2.Error())
		} else {
			log.Println("[MQTT Worker] Subscribed to topic: devices/+/status")
		}
	}

	opts.OnConnectionLost = func(client mqtt.Client, err error) {
		log.Printf("[MQTT Worker] Connection lost: %v", err)
	}

	client := mqtt.NewClient(opts)
	Client = client
	// Not waiting on the token: with ConnectRetry it never completes on failure, which would
	// deadlock main() before the HTTP server this worker depends on even starts.
	client.Connect()

	// Keep alive or clean up on context done
	go func() {
		<-ctx.Done()
		log.Println("[MQTT Worker] Context cancelled, disconnecting client...")
		client.Disconnect(250)
	}()
}

// PublishCommand sends a JSON command to devices/{mac}/command, e.g. {"cmd":"open_provisioning"}.
func PublishCommand(mac string, payload string) error {
	if Client == nil || !Client.IsConnected() {
		return fmt.Errorf("MQTT worker not connected to broker")
	}
	topic := fmt.Sprintf("devices/%s/command", mac)
	token := Client.Publish(topic, 1, false, payload)
	token.Wait()
	return token.Error()
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


	// Check pairing before persisting anything - an unpaired device leaves no new trace.
	device, err := repository.DB.GetDevice(ctx, mac)
	if err != nil || device == nil {
		log.Printf("[MQTT Worker] Device %s not registered (unpaired or unknown), discarding telemetry", mac)
		return
	}

	now := time.Now()

	// MongoDB keeps the raw, unsmoothed reading for clinical/historical accuracy.
	point := model.TelemetryDataPoint{
		Timestamp:   now.Unix(),
		BPM:         bpm,
		SPO2:        spo2,
		Temperature: temp,
		Humidity:    hum,
		Status:      evaluateStatus(bpm, spo2, temp),
	}

	date := now.Format("2006-01-02")
	hour := now.Hour()

	// Update in MongoDB hourly bucket (Always save every raw point for clinical accuracy in historical records)
	if err := repository.DB.UpdateTelemetryHistory(ctx, mac, date, hour, point); err != nil {
		log.Printf("[MQTT Worker] Failed to update telemetry bucket in MongoDB for device %s: %v", mac, err)
	} else {
		log.Printf("[MQTT Worker] Saved telemetry data to MongoDB bucket for device %s", mac)
	}

	// Firebase (live view + Warning/Normal status) uses the smoothed BPM instead - this
	// replaces the 4-sample rolling average the firmware used to compute on-device.
	smoothedBpm := rollingAverageBPM(mac, bpm)
	livePoint := point
	livePoint.BPM = smoothedBpm
	livePoint.Status = evaluateStatus(smoothedBpm, spo2, temp)

	// Update in Firebase Realtime Database under users/{ownerUID}/devices/{mac}/telemetry/latest
	if repository.Firebase != nil {
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
				bpmDiff := smoothedBpm - lastSent.BPM
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
				BPM:  smoothedBpm,
				SPO2: spo2,
				Temp: temp,
			}
			lastSentMutex.Unlock()

			if err := repository.Firebase.UpdateLiveTelemetry(ctx, device.OwnerUID, mac, livePoint); err != nil {
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

func handleStatusMessage(ctx context.Context, msg mqtt.Message) {
	topic := msg.Topic()
	payload := string(msg.Payload())

	log.Printf("[MQTT Worker] Received status update. Topic: %s, Payload: %s", topic, payload)

	// Topic pattern: devices/{mac}/status
	parts := strings.Split(topic, "/")
	if len(parts) < 3 {
		log.Printf("[MQTT Worker] Invalid status topic format: %s", topic)
		return
	}
	mac := parts[1]

	// Payload format: {"status": "online"} or {"status": "offline"}
	var statusData struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(payload), &statusData); err != nil {
		log.Printf("[MQTT Worker] Failed to parse status payload: %s", payload)
		return
	}

	isOnline := strings.ToLower(statusData.Status) == "online"

	if repository.Firebase != nil {
		device, err := repository.DB.GetDevice(ctx, mac)
		if err != nil || device == nil {
			log.Printf("[MQTT Worker] Device %s not found in MongoDB, skipping status update", mac)
		} else {
			if err := repository.Firebase.UpdateDeviceStatus(ctx, device.OwnerUID, mac, isOnline); err != nil {
				log.Printf("[MQTT Worker] Failed to update status in Firebase for device %s: %v", mac, err)
			} else {
				log.Printf("[MQTT Worker] Updated device status in Firebase to %v for device %s", isOnline, mac)
			}
		}
	}
}


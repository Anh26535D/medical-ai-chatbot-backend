package repository

import "os"

// Lives here, not in handler, so both handler and worker can import it without an import cycle.
const MQTTWorkerClientID = "medical_iot_backend_worker"

var MQTTWorkerSecret = func() string {
	secret := os.Getenv("MQTT_WORKER_SECRET")
	if secret == "" {
		return "insecure-default-worker-secret-change-me"
	}
	return secret
}()

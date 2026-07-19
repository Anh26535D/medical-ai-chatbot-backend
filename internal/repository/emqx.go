package repository

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"
)

// EMQXClient calls the EMQX Management API (distinct from the MQTT protocol itself),
// used to administratively act on live client sessions - currently just to kick a
// device's MQTT connection when it is unpaired from an account.
type EMQXClient struct {
	APIURL     string
	APIKey     string
	APISecret  string
	HTTPClient *http.Client
}

// Global EMQX Management API client instance, mirroring the Firebase/DB globals in this package.
var EMQX *EMQXClient

// InitEMQX initializes the global EMQX Management API client using environment variables.
// If EMQX_API_KEY/EMQX_API_SECRET are not set, EMQX stays nil and KickClient becomes a no-op
// (see the nil check there) so this integration is optional rather than a hard boot dependency.
func InitEMQX() {
	apiURL := os.Getenv("EMQX_API_URL")
	if apiURL == "" {
		apiURL = "http://emqx:18083/api/v5"
	}
	EMQX = &EMQXClient{
		APIURL:    apiURL,
		APIKey:    os.Getenv("EMQX_API_KEY"),
		APISecret: os.Getenv("EMQX_API_SECRET"),
		HTTPClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// KickClient forcibly disconnects a client's live MQTT session by client ID (the device's
// MAC address in this system). Used right after unpairing a device so a revoked access
// token takes effect immediately - without this, an already-connected device keeps
// publishing telemetry under the old pairing until its session happens to drop on its own
// (network blip, keepalive timeout, etc.), which could be an unbounded amount of time.
func (ec *EMQXClient) KickClient(ctx context.Context, clientID string) error {
	if ec == nil || ec.APIKey == "" || ec.APISecret == "" {
		return fmt.Errorf("EMQX Management API not configured")
	}

	url := fmt.Sprintf("%s/clients/%s", ec.APIURL, clientID)
	req, err := http.NewRequestWithContext(ctx, "DELETE", url, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(ec.APIKey, ec.APISecret)

	resp, err := ec.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 404 means the client simply isn't connected right now (e.g. WiFi already down) -
	// that is exactly the outcome we want, not a failure worth surfacing to the caller.
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("failed to kick MQTT client %s: status code %d", clientID, resp.StatusCode)
	}
	return nil
}

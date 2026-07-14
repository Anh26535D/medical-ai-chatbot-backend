package repository

import (
	"context"
	"testing"

	"medical-iot-backend/internal/model"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestUpdateTelemetryHistory(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().CreateClient(false))

	mt.Run("successful insert or update", func(mt *mtest.T) {
		db := &RealDatabase{
			MongoClient: mt.Client,
			MongoDbName: "test_db",
		}

		mt.AddMockResponses(mtest.CreateSuccessResponse())

		point := model.TelemetryDataPoint{
			Timestamp:   1700000000,
			BPM:         75,
			SPO2:        98,
			Temperature: 36.5,
			Humidity:    65.0,
			Status:      "Normal",
		}

		err := db.UpdateTelemetryHistory(context.Background(), "00:11:22:33:44:55", "2026-07-10", 16, point)
		assert.NoError(t, err)
	})
}

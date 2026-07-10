package repository

import (
	"context"
	"testing"

	"medical-iot-backend/internal/model"

	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/integration/mtest"
)

func TestUpdateTelemetryHistory(t *testing.T) {
	mt := mtest.New(t, mtest.NewOptions().CreateClient(false))
	defer mt.Close()

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

		// Inspect the commands sent to MongoDB
		sentEvents := mt.GetStartedEvents()
		assert.Len(t, sentEvents, 1)

		event := sentEvents[0]
		assert.Equal(t, "update", event.CommandName)

		// Parse the update command to check parameters
		var cmd struct {
			Update  string `bson:"update"`
			Updates []struct {
				Q bson.M `bson:"q"`
				U bson.M `bson:"u"`
			} `bson:"updates"`
		}
		err = bson.Unmarshal(event.Command, &cmd)
		assert.NoError(t, err)

		assert.Equal(t, "telemetry_history", cmd.Update)
		assert.Len(t, cmd.Updates, 1)

		// Check filter query (_id)
		expectedID := "00:11:22:33:44:55_2026-07-10_16"
		assert.Equal(t, expectedID, cmd.Updates[0].Q["_id"])

		// Check update operators: $setOnInsert and $push
		updateDoc := cmd.Updates[0].U
		setOnInsert, ok := updateDoc["$setOnInsert"].(bson.M)
		assert.True(t, ok)
		assert.Equal(t, "00:11:22:33:44:55", setOnInsert["mac_address"])
		assert.Equal(t, "2026-07-10", setOnInsert["date"])
		assert.Equal(t, int32(16), setOnInsert["hour"])

		push, ok := updateDoc["$push"].(bson.M)
		assert.True(t, ok)

		// Check pushed telemetry data point fields
		dataPoints, ok := push["data_points"].(bson.M)
		assert.True(t, ok)
		assert.Equal(t, int32(75), dataPoints["bpm"])
		assert.Equal(t, int32(98), dataPoints["spo2"])
		assert.Equal(t, 36.5, dataPoints["temp"])
		assert.Equal(t, 65.0, dataPoints["hum"])
		assert.Equal(t, "Normal", dataPoints["status"])
	})
}

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/server-sdk-go/v2/datatrack"
)

var host, apiKey, apiSecret, roomName, identity string

func init() {
	flag.StringVar(&host, "host", "", "livekit server host")
	flag.StringVar(&apiKey, "api-key", "", "livekit api key")
	flag.StringVar(&apiSecret, "api-secret", "", "livekit api secret")
	flag.StringVar(&roomName, "room-name", "", "room name")
	flag.StringVar(&identity, "identity", "publisher", "participant identity")
}

func main() {
	logger.InitFromConfig(&logger.Config{Level: "info"}, "datatrack-publisher")
	lksdk.SetLogger(logger.GetLogger())
	flag.Parse()
	if host == "" || apiKey == "" || apiSecret == "" || roomName == "" {
		fmt.Println("invalid arguments.")
		return
	}

	room, err := lksdk.ConnectToRoom(host, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: identity,
	}, nil)
	if err != nil {
		panic(err)
	}
	defer room.Disconnect()

	track, err := room.LocalParticipant.PublishDataTrack(context.Background(), "my_sensor_data")
	if err != nil {
		panic(err)
	}
	defer track.Unpublish()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pushFrames(ctx, track)
}

func readSensor() []byte {
	// Dynamically read some sensor data...
	return bytes.Repeat([]byte{0xfa}, 256)
}

func pushFrames(ctx context.Context, track *datatrack.LocalTrack) {
	for {
		logger.Infow("pushing frame")

		frame := datatrack.Frame{
			Payload:       readSensor(),
			UserTimestamp: datatrack.UserTimestampNow(),
		}
		if err := track.TryPush(frame); err != nil {
			logger.Warnw("failed to push frame", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

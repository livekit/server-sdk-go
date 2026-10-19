package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/server-sdk-go/v2/datatrack"
	"github.com/urfave/cli/v3"
)

func main() {
	logger.InitFromConfig(&logger.Config{Level: "info"}, "datatrack-publisher")
	lksdk.SetLogger(logger.GetLogger())

	cmd := &cli.Command{
		Name:  "datatrack-publisher",
		Usage: "publish sensor data on a data track",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "host", Usage: "livekit server host", Required: true, Sources: cli.EnvVars("LIVEKIT_URL")},
			&cli.StringFlag{Name: "api-key", Usage: "livekit api key", Required: true, Sources: cli.EnvVars("LIVEKIT_API_KEY")},
			&cli.StringFlag{Name: "api-secret", Usage: "livekit api secret", Required: true, Sources: cli.EnvVars("LIVEKIT_API_SECRET")},
			&cli.StringFlag{Name: "room-name", Usage: "room name", Required: true},
			&cli.StringFlag{Name: "identity", Usage: "participant identity", Value: "publisher"},
		},
		Action: run,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := cmd.Run(ctx, os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cmd *cli.Command) error {
	room, err := lksdk.ConnectToRoom(cmd.String("host"), lksdk.ConnectInfo{
		APIKey:              cmd.String("api-key"),
		APISecret:           cmd.String("api-secret"),
		RoomName:            cmd.String("room-name"),
		ParticipantIdentity: cmd.String("identity"),
	}, nil)
	if err != nil {
		return err
	}
	defer room.Disconnect()

	track, err := room.LocalParticipant.PublishDataTrack(ctx, "my_sensor_data")
	if err != nil {
		return err
	}
	defer track.Unpublish()

	pushFrames(ctx, track)
	return nil
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

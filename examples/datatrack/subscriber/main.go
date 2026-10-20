package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/livekit/server-sdk-go/v2/datatrack"
	"github.com/urfave/cli/v3"
)

func main() {
	logger.InitFromConfig(&logger.Config{Level: "info"}, "datatrack-subscriber")
	lksdk.SetLogger(logger.GetLogger())

	cmd := &cli.Command{
		Name:  "datatrack-subscriber",
		Usage: "subscribe to data tracks and log received frames",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "host", Usage: "livekit server host", Required: true, Sources: cli.EnvVars("LIVEKIT_URL")},
			&cli.StringFlag{Name: "api-key", Usage: "livekit api key", Required: true, Sources: cli.EnvVars("LIVEKIT_API_KEY")},
			&cli.StringFlag{Name: "api-secret", Usage: "livekit api secret", Required: true, Sources: cli.EnvVars("LIVEKIT_API_SECRET")},
			&cli.StringFlag{Name: "room-name", Usage: "room name", Required: true},
			&cli.StringFlag{Name: "identity", Usage: "participant identity", Value: "subscriber"},
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
	// Subscribe to any published data tracks
	callback := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnDataTrackPublished: func(track *datatrack.RemoteTrack, rp *lksdk.RemoteParticipant) {
				go subscribe(ctx, track)
			},
		},
	}

	room, err := lksdk.ConnectToRoom(cmd.String("host"), lksdk.ConnectInfo{
		APIKey:              cmd.String("api-key"),
		APISecret:           cmd.String("api-secret"),
		RoomName:            cmd.String("room-name"),
		ParticipantIdentity: cmd.String("identity"),
	}, callback)
	if err != nil {
		return err
	}
	defer room.Disconnect()

	<-ctx.Done()
	return nil
}

// subscribe subscribes to the given data track and logs received frames.
func subscribe(ctx context.Context, track *datatrack.RemoteTrack) {
	lgr := logger.GetLogger().WithValues("trackSID", track.Info().SID, "trackName", track.Info().Name, "publisher", track.PublisherIdentity())
	lgr.Infow("subscribing")

	stream, err := track.Subscribe(ctx)
	if err != nil {
		lgr.Warnw("failed to subscribe", err)
		return
	}
	defer stream.Close()

	for {
		frame, err := stream.Next(ctx)
		if err != nil {
			break
		}
		lgr.Infow("received frame", "bytes", len(frame.Payload))

		if latency, ok := frame.DurationSinceTimestamp(); ok {
			lgr.Infow("latency", "duration", latency)
		}
	}
	lgr.Infow("unsubscribed")
}

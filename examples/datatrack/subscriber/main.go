package main

import (
	"context"
	"flag"
	"fmt"
	"os/signal"
	"syscall"

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
	flag.StringVar(&identity, "identity", "subscriber", "participant identity")
}

func main() {
	logger.InitFromConfig(&logger.Config{Level: "info"}, "datatrack-subscriber")
	lksdk.SetLogger(logger.GetLogger())
	flag.Parse()
	if host == "" || apiKey == "" || apiSecret == "" || roomName == "" {
		fmt.Println("invalid arguments.")
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Subscribe to any published data tracks
	callback := &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnDataTrackPublished: func(track *datatrack.RemoteTrack, rp *lksdk.RemoteParticipant) {
				subscribe(ctx, track)
			},
		},
	}

	room, err := lksdk.ConnectToRoom(host, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: identity,
	}, callback)
	if err != nil {
		panic(err)
	}
	defer room.Disconnect()

	<-ctx.Done()
}

// subscribe subscribes to the given data track and logs received frames.
func subscribe(ctx context.Context, track *datatrack.RemoteTrack) {
	logger.Infow("subscribing", "track", track.Info().Name, "publisher", track.PublisherIdentity())

	stream, err := track.Subscribe(ctx)
	if err != nil {
		logger.Warnw("failed to subscribe", err)
		return
	}
	defer stream.Close()

	for frame := range stream.Frames() {
		logger.Infow("received frame", "bytes", len(frame.Payload))

		if latency, ok := frame.DurationSinceTimestamp(); ok {
			logger.Infow("latency", "duration", latency)
		}
	}
	logger.Infow("unsubscribed")
}

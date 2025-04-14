package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	res "github.com/livekit/mediatransportutil/pkg/audio/res"
	testdata "github.com/livekit/mediatransportutil/pkg/audio/res/testdata"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const (
	host                = "ws://localhost:7880"
	apiKey              = "devkey"
	apiSecret           = "secret"
	roomName            = "test"
	participantIdentity = "go-sdk-publisher"
)

func main() {
	logger.InitFromConfig(&logger.Config{Level: "info"}, "pcmopus")
	lksdk.SetLogger(logger.GetLogger())

	room, err := lksdk.ConnectToRoom(host, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: participantIdentity,
	}, nil)
	if err != nil {
		log.Fatal(err)
	}

	publishTrack, err := lksdk.NewPCM16ToOpusAudioTrack(48000, 20*time.Millisecond, logger.GetLogger())
	if err != nil {
		log.Fatal(err)
	}

	if _, err = room.LocalParticipant.PublishTrack(publishTrack, &lksdk.TrackPublicationOptions{
		Name: "test",
	}); err != nil {
		log.Fatal(err)
	}

	pcmSamples := res.ReadOggAudioFile(testdata.TestAudioOgg)
	for i := 0; i < 1000; i++ {
		for _, sample := range pcmSamples {
			err = publishTrack.WriteSample(sample)
			if err != nil {
				logger.Errorw("error writing sample", err)
			}
		}
	}

	defer func() {
		room.Disconnect()
		publishTrack.Close()
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)

	<-sigChan
}

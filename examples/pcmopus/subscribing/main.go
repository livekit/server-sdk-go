package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	webm "github.com/livekit/mediatransportutil/pkg/audio/webm"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"
)

const (
	host                = "ws://localhost:7880"
	apiKey              = "devkey"
	apiSecret           = "secret"
	roomName            = "test"
	participantIdentity = "go-sdk-subscriber"
)

func main() {
	logger.InitFromConfig(&logger.Config{Level: "info"}, "pcmopus")
	lksdk.SetLogger(logger.GetLogger())

	var pcmTrack *lksdk.OpusToPCM16AudioTrack
	var fileWriter *os.File

	room, err := lksdk.ConnectToRoom(host, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: participantIdentity,
	}, &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				pcmTrack, fileWriter = onTrackSubscribed(track, publication)
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		// TODO: This is racey, if writer is closed before room is disconnected,
		// it'll continue to read from the track and push to the writer, fix it.
		room.Disconnect()
		if pcmTrack != nil {
			pcmTrack.Close()
		}
		if fileWriter != nil {
			fileWriter.Close()
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)

	<-sigChan
}

func onTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication) (*lksdk.OpusToPCM16AudioTrack, *os.File) {
	fileWriter, err := os.Create("test-lksdk.mka")
	if err != nil {
		log.Fatal(err)
	}

	channels := 1
	if publication.TrackInfo().Stereo {
		channels = 2
	}

	webmWriter := webm.NewPCM16Writer(fileWriter, 48000, channels, 20*time.Millisecond)
	pcmTrack, err := lksdk.NewOpusToPCM16AudioTrack(track, publication, &webmWriter, 48000, true)
	if err != nil {
		log.Fatal(err)
	}

	return pcmTrack, fileWriter
}

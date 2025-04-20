package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/livekit/mediatransportutil/pkg/audio/webm"
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

	var pcmTrack *lksdk.DecodedAudioTrack
	var fileWriter *os.File

	room, err := lksdk.ConnectToRoom(host, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: participantIdentity,
	}, &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if track.Codec().MimeType == webrtc.MimeTypeOpus {
					pcmTrack, fileWriter = onTrackSubscribed(track, true)
				}
			},
			OnTrackUnsubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if pcmTrack != nil {
					pcmTrack.Close()
				}
				if fileWriter != nil {
					fileWriter.Close()
				}
			},
		},
	})
	if err != nil {
		panic(err)
	}
	defer func() {
		room.Disconnect()
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)

	<-sigChan
}

func onTrackSubscribed(track *webrtc.TrackRemote, forceMono bool) (*lksdk.DecodedAudioTrack, *os.File) {
	fileWriter, err := os.Create("test-final-stereo.mka")
	if err != nil {
		panic(err)
	}

	channels := lksdk.DetermineOpusChannels(track)
	if forceMono {
		channels = 1
	}

	webmWriter := webm.NewPCM16Writer(fileWriter, lksdk.DefaultOpusSampleRate, channels, lksdk.DefaultPCMSampleDuration)
	pcmTrack, err := lksdk.NewDecodedAudioTrack(track, channels, &webmWriter, lksdk.DefaultOpusSampleRate, forceMono)
	if err != nil {
		panic(err)
	}

	return pcmTrack, fileWriter
}

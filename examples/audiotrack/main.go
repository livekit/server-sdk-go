// TODO: document CGO behavior
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "embed"

	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	"github.com/pion/webrtc/v4"

	"github.com/livekit/media-sdk/res"
	"github.com/livekit/media-sdk/res/testdata"
	"github.com/livekit/media-sdk/webm"
)

//go:embed test.ogg
var Audio24k []byte

const (
	host      = "ws://localhost:7880"
	apiKey    = "devkey"
	apiSecret = "secret"
	roomName  = "test"
)

var (
	participantIdentity = "go-sdk"
	mode                string
	subscribePCMTrack   *lksdk.DecodingRemoteAudioTrack
	subscribeFileWriter *os.File
)

func init() {
	flag.StringVar(&mode, "mode", "publish", "publish or subscribe")
}

func connectToRoom(cb *lksdk.RoomCallback) (*lksdk.Room, error) {
	room, err := lksdk.ConnectToRoom(host, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: participantIdentity,
	}, cb)
	if err != nil {
		return nil, err
	}
	return room, nil
}

func getCbForRoom(publish bool) *lksdk.RoomCallback {
	if !publish {
		return &lksdk.RoomCallback{
			ParticipantCallback: lksdk.ParticipantCallback{
				OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
					if track.Codec().MimeType == webrtc.MimeTypeOpus {
						subscribePCMTrack, subscribeFileWriter = handleSubscribe(track)
					}
				},
				OnTrackUnsubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
					if subscribePCMTrack != nil {
						subscribePCMTrack.Close()
					}
					if subscribeFileWriter != nil {
						subscribeFileWriter.Close()
					}
				},
			},
		}
	}
	return nil
}

func main() {
	flag.Parse()

	logger.InitFromConfig(&logger.Config{Level: "info"}, "pcmopus")
	lksdk.SetLogger(logger.GetLogger())

	publish := mode == "publish"
	if publish {
		participantIdentity += "-publisher"
	} else {
		participantIdentity += "-subscriber"
	}

	room, err := connectToRoom(getCbForRoom(publish))
	if err != nil {
		panic(err)
	}
	defer room.Disconnect()

	if publish {
		go handlePublish(room)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)

	<-sigChan
}

func handlePublish(room *lksdk.Room) {
	publishTrack, err := lksdk.NewEncodingLocalAudioTrack(lksdk.DefaultOpusSampleRate, 1, logger.GetLogger())
	if err != nil {
		panic(err)
	}
	defer publishTrack.Close()

	if _, err = room.LocalParticipant.PublishTrack(publishTrack, &lksdk.TrackPublicationOptions{
		Name: "test",
	}); err != nil {
		panic(err)
	}

	pcmSamples := res.ReadOggAudioFile(testdata.TestAudioOgg, 48000, 1)
	for {
		for _, sample := range pcmSamples {
			err = publishTrack.WriteSample(sample)
			if err != nil {
				logger.Errorw("error writing sample", err)
			}
			// temp: some delay before writing next sample
			time.Sleep(25 * time.Millisecond)
		}
	}
}

func handleSubscribe(track *webrtc.TrackRemote) (*lksdk.DecodingRemoteAudioTrack, *os.File) {
	sampleRate := track.Codec().ClockRate
	fmt.Println("sampleRate", sampleRate)

	fileWriter, err := os.Create("test-reset-decoder.mka")
	if err != nil {
		panic(err)
	}

	channels := 2
	webmWriter := webm.NewPCM16Writer(fileWriter, lksdk.DefaultOpusSampleRate, channels, lksdk.DefaultOpusSampleDuration)
	pcmTrack, err := lksdk.NewDecodingRemoteAudioTrack(track, &webmWriter, lksdk.DefaultOpusSampleRate, channels)
	if err != nil {
		panic(err)
	}

	return pcmTrack, fileWriter
}

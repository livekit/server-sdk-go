package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/livekit/media-sdk"
	"github.com/livekit/protocol/logger"
	lksdk "github.com/livekit/server-sdk-go/v2"
	lkmedia "github.com/livekit/server-sdk-go/v2/pkg/media"
	"github.com/pion/webrtc/v4"
)

const (
	roomName  = "test-room"
	host      = "ws://localhost:7880"
	apiKey    = "devkey"
	apiSecret = "secret"
)

func callbacksForLkRoom(ws *WsManager) *lksdk.RoomCallback {
	var pcmRemoteTrack *lkmedia.PCMRemoteTrack

	return &lksdk.RoomCallback{
		ParticipantCallback: lksdk.ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
				if pcmRemoteTrack != nil {
					// only handle one track
					return
				}
				pcmRemoteTrack, _ = handleSubscribe(track, ws)
			},
		},
		OnDisconnected: func() {
			if pcmRemoteTrack != nil {
				pcmRemoteTrack.Close()
				pcmRemoteTrack = nil
			}
		},
		OnDisconnectedWithReason: func(reason lksdk.DisconnectionReason) {
			if pcmRemoteTrack != nil {
				pcmRemoteTrack.Close()
				pcmRemoteTrack = nil
			}
		},
	}
}

func main() {
	loadEnv()

	audioWriterChan := make(chan media.PCM16Sample)
	defer close(audioWriterChan)

	ws, err := NewWsManager(&WsManagerCallbacks{
		OnAudioReceived: func(audio media.PCM16Sample) {
			audioWriterChan <- audio
		},
	})
	if err != nil {
		panic(err)
	}
	defer ws.Close()

	room, err := connectToLKRoom(callbacksForLkRoom(ws))
	if err != nil {
		panic(err)
	}
	defer room.Disconnect()
	go handlePublish(room, audioWriterChan)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)

	<-sigChan
}

func handlePublish(room *lksdk.Room, audioWriterChan chan media.PCM16Sample) {
	publishTrack, err := lkmedia.NewPCMLocalTrack(24000, 1, logger.GetLogger())
	if err != nil {
		panic(err)
	}
	defer func() {
		publishTrack.ClearQueue()
		publishTrack.Close()
	}()

	if _, err = room.LocalParticipant.PublishTrack(publishTrack, &lksdk.TrackPublicationOptions{
		Name: "test",
	}); err != nil {
		panic(err)
	}

	for {
		select {
		case sample, ok := <-audioWriterChan:
			if !ok {
				return
			}

			if err := publishTrack.WriteSample(sample); err != nil {
				logger.Errorw("Failed to write sample", err)
			}
		}
	}
}

func handleSubscribe(track *webrtc.TrackRemote, ws *WsManager) (*lkmedia.PCMRemoteTrack, error) {
	fmt.Println("Handling subscribe")
	if track.Codec().MimeType != webrtc.MimeTypeOpus {
		logger.Warnw("Received non-opus track", nil, "track", track.Codec().MimeType)
	}

	writer := NewRemoteTrackWriter(ws)
	trackWriter, err := lkmedia.NewPCMRemoteTrack(track, writer, lkmedia.WithTargetSampleRate(24000))
	if err != nil {
		logger.Errorw("Failed to create remote track", err)
		return nil, err
	}

	return trackWriter, nil
}

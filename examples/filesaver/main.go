package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/livekit/protocol/auth"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"
	"github.com/livekit/server-sdk-go/pkg/trackrecorder"
	"github.com/pion/webrtc/v3"
)

var (
	host, apiKey, apiSecret, roomName, identity string
)

func init() {
	flag.StringVar(&host, "host", "", "livekit server host")
	flag.StringVar(&apiKey, "api-key", "", "livekit api key")
	flag.StringVar(&apiSecret, "api-secret", "", "livekit api secret")
	flag.StringVar(&roomName, "room-name", "", "room name")
	flag.StringVar(&identity, "identity", "", "participant identity")
}

func main() {
	flag.Parse()
	if host == "" || apiKey == "" || apiSecret == "" || roomName == "" || identity == "" {
		fmt.Println("invalid arguments.")
		return
	}

	token, err := createSubscriberToken(apiKey, apiSecret, identity)
	if err != nil {
		panic(err)
	}

	b, err := createBot(host, token)
	if err != nil {
		panic(err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT)

	<-sigChan
	b.Disconnect()
}

func createSubscriberToken(key string, secret string, id string) (string, error) {
	at := auth.NewAccessToken(apiKey, apiSecret)
	f := false
	t := true
	grant := &auth.VideoGrant{
		Room:           roomName,
		RoomJoin:       true,
		CanPublish:     &f,
		CanPublishData: &f,
		CanSubscribe:   &t,
		Hidden:         true,
	}
	return at.
		AddGrant(grant).
		SetIdentity(identity).
		SetValidFor(time.Hour).
		ToJWT()
}

type bot struct {
	recorders map[string]trackrecorder.Recorder
	room      *lksdk.Room
}

func createBot(url string, token string) (*bot, error) {
	b := &bot{
		recorders: make(map[string]trackrecorder.Recorder),
	}

	room, err := lksdk.ConnectToRoomWithToken(host, token)
	if err != nil {
		return nil, err
	}

	room.Callback.OnTrackSubscribed = b.OnTrackSubscribed
	room.Callback.OnTrackUnsubscribed = b.OnTrackUnsubscribed
	b.room = room

	return b, nil
}

func (b *bot) Disconnect() {
	for _, r := range b.recorders {
		r.Stop()
	}
	b.room.Disconnect()
}

func (b *bot) OnTrackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	// Don't need check empty string (unsupported codec). We'll catch this error in trackrecorder.New()
	fileExt := trackrecorder.GetMediaExtension(track.Codec().MimeType)
	fileName := fmt.Sprintf("%s-%s.%s", rp.Identity(), track.ID(), fileExt)

	r, err := trackrecorder.New(fileName, track.Codec(), samplebuilder.WithPacketDroppedHandler(func() {
		rp.WritePLI(track.SSRC())
	}))
	if err != nil {
		log.Println("trackrecorder error: ", err)
		return
	}

	r.Start(context.TODO(), track)
	b.recorders[publication.SID()] = r
}

func (b *bot) OnTrackUnsubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
	_, found := b.recorders[publication.SID()]
	if !found {
		return
	}

	r := b.recorders[publication.SID()]
	r.Stop()
	delete(b.recorders, publication.SID())
}

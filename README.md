# LiveKit Go SDK

This is the official Golang SDK to [LiveKit](https://docs.livekit.io). You would integrate this on your app's backend in order to

- Create access tokens
- Access LiveKit server-side APIs, giving you moderation capabilities
- Client SDK to interact as participant, publish & record room streams
- Receive [webhook](https://docs.livekit.io/guides/webhooks/) callbacks

## Token creation

```go
import (
	"time"

	lksdk "github.com/livekit/server-sdk-go"
	"github.com/livekit/protocol/auth"
)

func getJoinToken(apiKey, apiSecret, room, identity string) (string, error) {
	at := auth.NewAccessToken(apiKey, apiSecret)
	grant := &auth.VideoGrant{
		RoomJoin: true,
		Room:     room,
	}
	at.AddGrant(grant).
		SetIdentity(identity).
		SetValidFor(time.Hour)

	return at.ToJWT()
}
```

## RoomService API

RoomService gives you complete control over rooms and participants within them. It includes selective track subscriptions as well as moderation capabilities.

```go
import (
	lksdk "github.com/livekit/server-sdk-go"
	livekit "github.com/livekit/protocol/proto"
)

func main() {
	host := "host"
	apiKey := "key"
	apiSecret := "secret"

	roomName := "myroom"
	identity := "participantIdentity"

    roomClient := lksdk.NewRoomServiceClient(host, apiKey, apiSecret)

    // create a new room
    room, _ := roomClient.CreateRoom(context.Background(), &livekit.CreateRoomRequest{
		Name: roomName,
	})

    // list rooms
    res, _ := roomClient.ListRooms(context.Background(), &livekit.ListRoomsRequest{})

    // terminate a room and cause participants to leave
    roomClient.DeleteRoom(context.Background(), &livekit.DeleteRoomRequest{
		Room: roomId,
	})

    // list participants in a room
    res, _ := roomClient.ListParticipants(context.Background(), &livekit.ListParticipantsRequest{
		Room: roomName,
	})

    // disconnect a participant from room
    roomClient.RemoveParticipant(context.Background(), &livekit.RoomParticipantIdentity{
		Room:     roomName,
		Identity: identity,
	})

    // mute/unmute participant's tracks
    roomClient.MutePublishedTrack(context.Background(), &livekit.MuteRoomTrackRequest{
		Room:     roomName,
		Identity: identity,
		TrackSid: "track_sid",
		Muted:    true,
	})
}
```

## Interacting as a participant

The Participant SDK gives you access programmatic access as a client enabling you to publish and record audio/video/data to the room.

```go
import (
  lksdk "github.com/livekit/server-sdk-go"
)

func main() {
  host := "<host>"
  apiKey := "api-key"
  apiSecret := "api-secret"
  roomName := "myroom"
  identity := "botuser"
	room, err := lksdk.ConnectToRoom(host, lksdk.ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: identity,
	})
	if err != nil {
		panic(err)
	}

  room.Callback.OnTrackSubscribed = trackSubscribed
  ...
  room.Disconnect()
}

func trackSubscribed(track *webrtc.TrackRemote, publication lksdk.TrackPublication, rp *lksdk.RemoteParticipant) {

}
```

## Publishing tracks to Room

With the Go SDK, you can publish existing files encoded in H.264, VP8, and Opus to the room.

First, you will need to encode media into the right format.

### VP8 / Opus

```bash
INPUT_FILE=<file> \
OUTPUT_VP8=<file> \
OUTPUT_OGG=<file> \
ffmpeg -i $INPUT_FILE \
  -c:v libvpx -keyint_min 120 -qmax 50 -maxrate 2M -b:v 1M $OUTPUT_VP8 \
  -c:a libopus -page_duration 20000 -vn $OUTPUT_OGG
```

The above encodes VP8 at average 1Mbps / max 2Mbps with a minimum keyframe interval of 120.  

### H.264 / Opus

```bash
INPUT_FILE=<file> \
OUTPUT_H264=<file> \
OUTPUT_OGG=<file> \
ffmpeg -i $INPUT_FILE
  -c:v libx264 -bsf:v h264_mp4toannexb -b:v 2M -x264-params keyint=120 -max_delay 0 -bf 0 $OUTPUT_H264 \
  -c:a libopus -page_duration 20000 -vn $OUTPUT_OGG
```

The above encodes VP8 a CBS of 2Mbps with a minimum keyframe interval of 120.

### Publish from file

```go
file := "video.ivf"
track, err := lksdk.NewLocalFileTrack(file,
	// control FPS to ensure synchronization
	lksdk.FileTrackWithFrameDuration(33 * time.Millisecond),
	lksdk.FileTrackWithOnWriteComplete(func() { fmt.Println("track finished") }),
)
if err != nil {
    return err
}
if _, err = room.LocalParticipant.PublishTrack(track, file); err != nil {
    return err
}
```

For a full working example, refer to [join.go](https://github.com/livekit/livekit-cli/blob/main/cmd/livekit-cli/join.go) in livekit-cli.

### Publish from other sources

In order to publish from non-file sources, you will have to implement your own `SampleProvider`, that could provide frames of data with a `NextSample` method.

The SDK takes care of sending the samples to the room.

## Receiving webhooks

The Go SDK helps you to verify and decode webhook callbacks to ensure their authenticity.
See [webhooks guide](https://docs.livekit.io/guides/webhooks) for configuration.

```go
import (
	livekit "github.com/livekit/protocol/proto"
	"github.com/livekit/protocol/webhook"
	"google.golang.org/protobuf/encoding/protojson"
)

func ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, err := webhook.Receive(r, s.provider)
	if err != nil {
		// could not validate, handle error
		return
	}

	event := livekit.WebhookEvent{}
	if err = protojson.Unmarshal(data, &event); err != nil {
		// handle error
		return
	}

	// consume WebhookEvent
}
```

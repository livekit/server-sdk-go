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
	livekit "github.com/livekit/protocol/livekit"
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
OUTPUT_VP8=<output.ivf> \
OUTPUT_OGG=<output.ogg> \
ffmpeg -i $INPUT_FILE \
  -c:v libvpx -keyint_min 120 -qmax 50 -maxrate 2M -b:v 1M $OUTPUT_VP8 \
  -c:a libopus -page_duration 20000 -vn $OUTPUT_OGG
```

The above encodes VP8 at average 1Mbps / max 2Mbps with a minimum keyframe interval of 120.

### H.264 / Opus

```bash
INPUT_FILE=<file> \
OUTPUT_H264=<output.h264> \
OUTPUT_OGG=<output.ogg> \
ffmpeg -i $INPUT_FILE
  -c:v libx264 -bsf:v h264_mp4toannexb -b:v 2M -x264-params keyint=120 -max_delay 0 -bf 0 $OUTPUT_H264 \
  -c:a libopus -page_duration 20000 -vn $OUTPUT_OGG
```

The above encodes H264 with CBS of 2Mbps with a minimum keyframe interval of 120.

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
	livekit "github.com/livekit/protocol/livekit"
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

## Recording a room via RecordBot

RecordBot is suitable if you want to record tracks separately (`video-only`, `audio-only` or `split-audio-video`). It does this by the following steps:

1. Joining the room as a bot. We can hide the bot by not rendering any participants with the same name of the bot (default is `recordbot`).
2. Writing RTP packets directly to file, relying on `MediaWriter` interface in `ion-sfu`. The file ID (without extension) generator is passed in a hook.
3. Optionally upload to cloud via hook.

Unfortunately, RecordBot will not be able to do compositing of tracks. If you require this feature, the original [livekit-recorder](https://github.com/livekit/livekit-recorder) is your solution.

Note: Each room must only have 1 RecordBot as RecordBot manages multiple recorders for each participant and track.

```go
import "github.com/livekit/server-sdk-go/pkg/recordbot"

func main() {
	config := recordbot.RecordBotConfig{
		BaseUrl:   "LIVEKIT_URL",
		ApiKey:    "LIVEKIT_API_KEY",
		ApiSecret: "LIVEKIT_API_SECRET",
		BotName:   "recordbot",
		Hooks: recordbot.RecordBotHooks{
			GenerateFileID: func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) string {
				// Example: user1--2022_01_19-11_38_23
				return fmt.Sprintf("%s--%d_%02d_%02d-%02d_%02d_%02d",
					rp.Identity(),
					current.Year(), current.Month(), current.Day(),
					current.Hour(), current.Minute(), current.Second())
			}
		},
		OutputType: recordbot.OutputVideoOnly
	}

	// When a bot is created, it will:
	// - Connect to LiveKit room as a participant
	// - Attach 2 callbacks:
	//   a) Create a recorder and start recording anytime a track is subscribed
	//   b) When a track is unsubscribed, stop recording and save the file
	bot, err := recordbot.NewRecordBot("my_room", config)

	// When Disconnect() is called, it will:
	// - Stop all recorders and save the respective files
	// - Exit the LiveKit room
	bot.Disconnect()
}
```

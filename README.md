<!--BEGIN_BANNER_IMAGE-->

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="/.github/banner_dark.png">
  <source media="(prefers-color-scheme: light)" srcset="/.github/banner_light.png">
  <img style="width:100%;" alt="The LiveKit icon, the name of the repository and some sample code in the background." src="https://raw.githubusercontent.com/livekit/server-sdk-go/main/.github/banner_light.png">
</picture>

<!--END_BANNER_IMAGE-->

# LiveKit Go SDK

<!--BEGIN_DESCRIPTION-->
Use this SDK to interact with <a href="https://livekit.io/">LiveKit</a> server APIs and create access tokens from your Go backend.
<!--END_DESCRIPTION-->

[!NOTE]

Version 2 of the <platform> SDK contains a small set of breaking changes. Read the [migration guide](https://docs.livekit.io/guides/migrate-from-v1/) for a detailed overview of what has changed.

## Installation

```shell
go get github.com/livekit/server-sdk-go/v2
```

Note: since v1.0 release, this package requires Go 1.18+ in order to build.

## Token creation

```go
import (
	"time"

	lksdk "github.com/livekit/server-sdk-go/v2"
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
	lksdk "github.com/livekit/server-sdk-go/v2"
	livekit "github.com/livekit/protocol/livekit"
)

func main() {
	hostURL := "host-url"  // ex: https://project-123456.livekit.cloud
	apiKey := "api-key"
	apiSecret := "api-secret"

	roomName := "myroom"
	identity := "participantIdentity"

    roomClient := lksdk.NewRoomServiceClient(hostURL, apiKey, apiSecret)

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

The Real-time SDK gives you access programmatic access as a client enabling you to publish and record audio/video/data to the room.

```go
import (
  lksdk "github.com/livekit/server-sdk-go/v2"
)

func main() {
  hostURL := "host-url"  // ex: https://project-123456.livekit.cloud
  apiKey := "api-key"
  apiSecret := "api-secret"
  roomName := "myroom"
  identity := "botuser"
  roomCB := &lksdk.RoomCallback{
	ParticipantCallback: lksdk.ParticipantCallback{
	  OnTrackSubscribed: trackSubscribed,
  	},
  }
  room, err := lksdk.ConnectToRoom(hostURL, lksdk.ConnectInfo{
  	APIKey:              apiKey,
  	APISecret:           apiSecret,
  	RoomName:            roomName,
  	ParticipantIdentity: identity,
  }, roomCB)
  if err != nil {
  	panic(err)
  }

  ...
  room.Disconnect()
}

func trackSubscribed(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {

}
```

For more advanced usage, see the [examples](https://github.com/livekit/server-sdk-go/tree/main/examples) directory.

## Publishing tracks to Room

With the Go SDK, you can publish existing files encoded in H.264, VP8, and Opus to the room.

First, you will need to encode media into the right format.

### VP8 / Opus

```bash
ffmpeg -i <input.mp4> \
  -c:v libvpx -keyint_min 120 -qmax 50 -maxrate 2M -b:v 1M <output.ivf> \
  -c:a libopus -page_duration 20000 -vn <output.ogg>
```

The above encodes VP8 at average 1Mbps / max 2Mbps with a minimum keyframe interval of 120.

### H.264 / Opus

```bash
ffmpeg -i <input.mp4> \
  -c:v libx264 -bsf:v h264_mp4toannexb -b:v 2M -profile baseline -pix_fmt yuv420p \
    -x264-params keyint=120 -max_delay 0 -bf 0 <output.h264> \
  -c:a libopus -page_duration 20000 -vn <output.ogg>
```

The above encodes H264 with CBS of 2Mbps with a minimum keyframe interval of 120.

### Publish from file

```go
file := "video.ivf"
videoWidth := 1920
videoHeight := 1080
track, err := lksdk.NewLocalFileTrack(file,
	// control FPS to ensure synchronization
	lksdk.ReaderTrackWithFrameDuration(33 * time.Millisecond),
	lksdk.ReaderTrackWithOnWriteComplete(func() { fmt.Println("track finished") }),
)
if err != nil {
    return err
}
if _, err = room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{
	Name: file,
	VideoWidth: videoWidth,
	VideoHeight: videoHeight,
}); err != nil {
    return err
}
```

### Publish from io.ReadCloser implementation

```go
// - `in` implements io.ReadCloser, such as buffer or file
// - `mime` has to be one of webrtc.MimeType...
track, err := lksdk.NewLocalReaderTrack(in, mime,
	lksdk.ReaderTrackWithFrameDuration(33 * time.Millisecond),
	lksdk.ReaderTrackWithOnWriteComplete(func() { fmt.Println("track finished") }),
)
if err != nil {
	return err
}
if _, err = room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{}); err != nil {
    return err
}
```

For a full working example, refer to [filesender](https://github.com/livekit/server-sdk-go/blob/main/examples/filesender/main.go). This
example sends all audio/video files in the current directory.

### Publish from other sources

In order to publish from non-file sources, you will have to implement your own `SampleProvider`, that could provide frames of data with a `NextSample` method.

The SDK takes care of sending the samples to the room.

### Using a pacer

With video publishing, keyframes can be an order of magnitude larger than delta frames.
This size difference can cause a significant increase in bitrate when a keyframe is transmitted, leading to a surge in packet flow.
Such spikes might result in packet loss at the forwarding layer. To maintain a consistent packet flow,
you can enable the use of a [pacer](https://chromium.googlesource.com/external/webrtc/+/master/modules/pacing/g3doc/index.md).

```go
import "github.com/livekit/mediatransportutil/pkg/pacer"

// Control total output bitrate to 10Mbps with 1s max latency
pf := pacer.NewPacerFactory(
	pacer.LeakyBucketPacer,
	pacer.WithBitrate(10000000),
	pacer.WithMaxLatency(time.Second),
)

room, err := lksdk.ConnectToRoom(hostURL, lksdk.ConnectInfo{
    APIKey:              apiKey,
    APISecret:           apiSecret,
    RoomName:            roomName,
    ParticipantIdentity: identity,
}, &lksdk.RoomCallback{
    ParticipantCallback: lksdk.ParticipantCallback{
        OnTrackSubscribed: onTrackSubscribed,
    },
}, lksdk.WithPacer(pf))
```

## Receiving tracks from Room

With the Go SDK, you can accept media from the room.

For a full working example, refer to [filesaver](https://github.com/livekit/server-sdk-go/blob/main/examples/filesaver/main.go). This
example saves the audio/video in the LiveKit room to the local disk.


## Receiving webhooks

The Go SDK helps you to verify and decode webhook callbacks to ensure their authenticity.
See [webhooks guide](https://docs.livekit.io/guides/webhooks) for configuration.

```go
import (
	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/webhook"
)

var authProvider = auth.NewSimpleKeyProvider(
	apiKey, apiSecret,
)

func ServeHTTP(w http.ResponseWriter, r *http.Request) {
  // event is a livekit.WebhookEvent{} object
	event, err := webhook.ReceiveWebhookEvent(r, authProvider)
	if err != nil {
		// could not validate, handle error
		return
	}

	// consume WebhookEvent
}
```

<!--BEGIN_REPO_NAV-->
<br/><table>
<thead><tr><th colspan="2">LiveKit Ecosystem</th></tr></thead>
<tbody>
<tr><td>LiveKit SDKs</td><td><a href="https://github.com/livekit/client-sdk-js">Browser</a> · <a href="https://github.com/livekit/client-sdk-swift">iOS/macOS/visionOS</a> · <a href="https://github.com/livekit/client-sdk-android">Android</a> · <a href="https://github.com/livekit/client-sdk-flutter">Flutter</a> · <a href="https://github.com/livekit/client-sdk-react-native">React Native</a> · <a href="https://github.com/livekit/rust-sdks">Rust</a> · <a href="https://github.com/livekit/node-sdks">Node.js</a> · <a href="https://github.com/livekit/python-sdks">Python</a> · <a href="https://github.com/livekit/client-sdk-unity">Unity</a> · <a href="https://github.com/livekit/client-sdk-unity-web">Unity (WebGL)</a></td></tr><tr></tr>
<tr><td>Server APIs</td><td><a href="https://github.com/livekit/node-sdks">Node.js</a> · <b>Golang</b> · <a href="https://github.com/livekit/server-sdk-ruby">Ruby</a> · <a href="https://github.com/livekit/server-sdk-kotlin">Java/Kotlin</a> · <a href="https://github.com/livekit/python-sdks">Python</a> · <a href="https://github.com/livekit/rust-sdks">Rust</a> · <a href="https://github.com/agence104/livekit-server-sdk-php">PHP (community)</a> · <a href="https://github.com/pabloFuente/livekit-server-sdk-dotnet">.NET (community)</a></td></tr><tr></tr>
<tr><td>UI Components</td><td><a href="https://github.com/livekit/components-js">React</a> · <a href="https://github.com/livekit/components-android">Android Compose</a> · <a href="https://github.com/livekit/components-swift">SwiftUI</a></td></tr><tr></tr>
<tr><td>Agents Frameworks</td><td><a href="https://github.com/livekit/agents">Python</a> · <a href="https://github.com/livekit/agents-js">Node.js</a> · <a href="https://github.com/livekit/agent-playground">Playground</a></td></tr><tr></tr>
<tr><td>Services</td><td><a href="https://github.com/livekit/livekit">LiveKit server</a> · <a href="https://github.com/livekit/egress">Egress</a> · <a href="https://github.com/livekit/ingress">Ingress</a> · <a href="https://github.com/livekit/sip">SIP</a></td></tr><tr></tr>
<tr><td>Resources</td><td><a href="https://docs.livekit.io">Docs</a> · <a href="https://github.com/livekit-examples">Example apps</a> · <a href="https://livekit.io/cloud">Cloud</a> · <a href="https://docs.livekit.io/home/self-hosting/deployment">Self-hosting</a> · <a href="https://github.com/livekit/livekit-cli">CLI</a></td></tr>
</tbody>
</table>
<!--END_REPO_NAV-->

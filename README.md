# LiveKit Go SDK

This is an SDK to [LiveKit](https://docs.livekit.io). It gives you the following capabilities:

- API access to LiveKit RoomService
- Create access tokens
- Command line utility: `livekit-cli`
- Client SDK to interact as participant, publish & record room streams

## Token creation

```go
import (
	"time"

	lksdk "github.com/livekit/livekit-sdk-go"
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
	lksdk "github.com/livekit/livekit-sdk-go"
	livekit "github.com/livekit/livekit-sdk-go/proto"
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

## CLI

CLI serves as a convenient way of accessing RoomService capabilities.

```shell
% ./bin/livekit-cli --help
NAME:
   livekit-cli - CLI client to LiveKit

USAGE:
   livekit-cli [global options] command [command options] [arguments...]

VERSION:
   0.5.0

COMMANDS:
   create-token        creates an access token
   create-room
   list-rooms
   delete-room
   list-participants
   get-participant
   remove-participant
   mute-track
   join-room           joins a room as a client
   help, h             Shows a list of commands or help for one command
```

## Interacting as a participant

The Participant SDK gives you access programmatic access as a client enabling you to publish and record audio/video/data to the room.

```go
import (
  lksdk "github.com/livekit/livekit-sdk-go"
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

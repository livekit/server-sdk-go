package temp

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	lksdk "github.com/livekit/server-sdk-go/v2"
)

func TestPublish(t *testing.T) {
	room, err := lksdk.ConnectToRoom("wss"+os.Getenv("LIVEKIT_URL")[5:], lksdk.ConnectInfo{
		APIKey:              os.Getenv("LIVEKIT_API_KEY"),
		APISecret:           os.Getenv("LIVEKIT_API_SECRET"),
		RoomName:            "av1-sample-test",
		ParticipantName:     "av1-publisher",
		ParticipantIdentity: "av1-publisher",
	}, lksdk.NewRoomCallback())
	require.NoError(t, err)

	now := time.Now()
	done := make(chan struct{})
	var pub *lksdk.LocalTrackPublication
	opts := []lksdk.ReaderSampleProviderOption{
		lksdk.ReaderTrackWithOnWriteComplete(func() {
			fmt.Println("write complete", time.Since(now))
			close(done)
			if pub != nil {
				_ = room.LocalParticipant.UnpublishTrack(pub.SID())
			}
		}),
	}

	opts = append(opts, lksdk.ReaderTrackWithFrameDuration(time.Microsecond*41708))

	track, err := lksdk.NewLocalFileTrack("../../../egress/test/sample/matrix-trailer-av1.ivf", opts...)
	require.NoError(t, err)

	pub, err = room.LocalParticipant.PublishTrack(track, &lksdk.TrackPublicationOptions{Name: "matrix-trailer-av1.ivf"})
	require.NoError(t, err)

	<-done
}

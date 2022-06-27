package lksdk

import (
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

// The integration test of the SDK. can't run this test standalone, should be run with `mage test`

const (
	host = "ws://localhost:7880"
)

var (
	apiKey, apiSecret string
)

func TestMain(m *testing.M) {
	keys := strings.Split(os.Getenv("LIVEKIT_KEYS"), ": ")
	apiKey, apiSecret = keys[0], keys[1]

	os.Exit(m.Run())
}

func createAgent(roomName string, callback *RoomCallback, name string) (*Room, error) {
	room, err := ConnectToRoom(host, ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: name,
	}, callback)
	if err != nil {
		return nil, err
	}
	return room, nil
}

func pubNullTrack(t *testing.T, room *Room, name string) *LocalTrackPublication {
	track, err := NewLocalSampleTrack(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		Channels:  2,
		ClockRate: 48000,
	})
	require.NoError(t, err)
	provider := &NullSampleProvider{
		BytesPerSample: 1000,
		SampleDuration: 50 * time.Millisecond,
	}

	track.OnBind(func() {
		if err := track.StartWrite(provider, func() {}); err != nil {
			logger.Error(err, "Could not start writing")
		}
	})

	localPub, err := room.LocalParticipant.PublishTrack(track, &TrackPublicationOptions{
		Name: name,
	})
	require.NoError(t, err)
	return localPub
}

func TestJoin(t *testing.T) {
	pub, err := createAgent(t.Name(), nil, "publisher")
	require.NoError(t, err)

	var dataLock sync.Mutex
	var receivedData string

	audioTrackName := "audio_of_pub1"
	var trackLock sync.Mutex
	var trackReceived atomic.Bool

	subCB := &RoomCallback{
		ParticipantCallback: ParticipantCallback{
			OnDataReceived: func(data []byte, rp *RemoteParticipant) {
				dataLock.Lock()
				receivedData = string(data)
				dataLock.Unlock()
			},
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant) {
				trackLock.Lock()
				trackReceived.Store(true)
				require.Equal(t, rp.Name(), pub.LocalParticipant.Name())
				require.Equal(t, publication.Name(), audioTrackName)
				require.Equal(t, track.Kind(), webrtc.RTPCodecTypeAudio)
				trackLock.Unlock()
			},
		},
	}
	sub, err := createAgent(t.Name(), subCB, "subscriber")
	require.NoError(t, err)

	pub.LocalParticipant.PublishData([]byte("test"), livekit.DataPacket_RELIABLE, nil)
	localPub := pubNullTrack(t, pub, audioTrackName)
	require.Equal(t, localPub.Name(), audioTrackName)

	require.Eventually(t, func() bool { return trackReceived.Load() }, 5*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool {
		dataLock.Lock()
		defer dataLock.Unlock()
		return receivedData == "test"
	}, 5*time.Second, 100*time.Millisecond)

	pub.Disconnect()
	sub.Disconnect()
}

func TestResume(t *testing.T) {
	var reconnected atomic.Bool
	pubCB := &RoomCallback{
		OnReconnected: func() {
			reconnected.Store(true)
		},
	}
	pub, err := createAgent(t.Name(), pubCB, "publisher")
	require.NoError(t, err)

	// test pub sub after reconnected
	audioTrackName := "audio_of_pub1"
	var trackLock sync.Mutex
	var trackReceived atomic.Bool
	subCB := &RoomCallback{
		ParticipantCallback: ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant) {
				trackLock.Lock()
				trackReceived.Store(true)
				require.Equal(t, rp.Name(), pub.LocalParticipant.Name())
				require.Equal(t, publication.Name(), audioTrackName)
				require.Equal(t, track.Kind(), webrtc.RTPCodecTypeAudio)
				trackLock.Unlock()
			},
		},
	}
	sub, err := createAgent(t.Name(), subCB, "subscriber")
	require.NoError(t, err)

	pub.Simulate(SimulateSignalReconnect)
	require.Eventually(t, func() bool { return reconnected.Load() }, 5*time.Second, 100*time.Millisecond)

	logger.Info("reconnected")

	localPub := pubNullTrack(t, pub, audioTrackName)
	require.Equal(t, localPub.Name(), audioTrackName)
	require.Eventually(t, func() bool { return trackReceived.Load() }, 5*time.Second, 100*time.Millisecond)

	pub.Disconnect()
	sub.Disconnect()
}

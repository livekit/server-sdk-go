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
	// keys := strings.Split("APIhdZda4TDAGxk: 4BA2qCorbmGnVZ9iMri7Sp0EEA7v2S4Oi8eyHuPxtxJ", ": ")
	apiKey, apiSecret = keys[0], keys[1]

	os.Exit(m.Run())
}

func createAgent(roomName string, name ...string) ([]*Room, error) {
	var rooms []*Room
	for _, n := range name {
		room, err := ConnectToRoom(host, ConnectInfo{
			APIKey:              apiKey,
			APISecret:           apiSecret,
			RoomName:            roomName,
			ParticipantIdentity: n,
		})
		if err != nil {
			return rooms, err
		}
		rooms = append(rooms, room)
	}
	return rooms, nil
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
	rooms, err := createAgent(t.Name(), "publisher", "subscriber")
	require.NoError(t, err)
	pub, sub := rooms[0], rooms[1]

	var dataLock sync.Mutex
	var receivedData string
	sub.Callback.OnDataReceived = func(data []byte, rp *RemoteParticipant) {
		dataLock.Lock()
		receivedData = string(data)
		dataLock.Unlock()
	}

	audioTrackName := "audio_of_pub1"
	var trackLock sync.Mutex
	var trackReceived bool
	sub.Callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant) {
		trackLock.Lock()
		trackReceived = true
		require.Equal(t, rp.Name(), pub.LocalParticipant.Name())
		require.Equal(t, publication.Name(), audioTrackName)
		require.Equal(t, track.Kind(), webrtc.RTPCodecTypeAudio)
		trackLock.Unlock()
	}

	pub.LocalParticipant.PublishData([]byte("test"), livekit.DataPacket_RELIABLE, nil)
	localPub := pubNullTrack(t, pub, audioTrackName)
	require.Equal(t, localPub.Name(), audioTrackName)

	require.Eventually(t, func() bool { return trackReceived }, 5*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool {
		dataLock.Lock()
		defer dataLock.Unlock()
		return receivedData == "test"
	}, 5*time.Second, 100*time.Millisecond)

	for _, room := range rooms {
		room.Disconnect()
	}
}

func TestResume(t *testing.T) {
	rooms, err := createAgent(t.Name(), "publisher", "subscriber")
	require.NoError(t, err)
	pub, sub := rooms[0], rooms[1]
	var reconnected atomic.Bool
	pub.Callback.OnReconnected = func() {
		reconnected.Store(true)
	}
	pub.Simulate(SimulateSignalReconnect)
	require.Eventually(t, func() bool { return reconnected.Load() }, 500*time.Second, 100*time.Millisecond)

	logger.Info("reconnected")

	// test pub sub after reconnected
	audioTrackName := "audio_of_pub1"
	var trackLock sync.Mutex
	var trackReceived bool
	sub.Callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant) {
		trackLock.Lock()
		trackReceived = true
		require.Equal(t, rp.Name(), pub.LocalParticipant.Name())
		require.Equal(t, publication.Name(), audioTrackName)
		require.Equal(t, track.Kind(), webrtc.RTPCodecTypeAudio)
		trackLock.Unlock()
	}

	localPub := pubNullTrack(t, pub, audioTrackName)
	require.Equal(t, localPub.Name(), audioTrackName)
	require.Eventually(t, func() bool { return trackReceived }, 5*time.Second, 100*time.Millisecond)

	for _, room := range rooms {
		room.Disconnect()
	}
}

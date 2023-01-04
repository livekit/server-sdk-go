package lksdk

import (
	"errors"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/livekit"
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
			logger.Errorw("Could not start writing", err)
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
	serverInfo := sub.ServerInfo()
	require.NotNil(t, serverInfo)
	require.Equal(t, serverInfo.Edition, livekit.ServerInfo_Standard)

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

	logger.Infow("reconnected")

	localPub := pubNullTrack(t, pub, audioTrackName)
	require.Equal(t, localPub.Name(), audioTrackName)
	require.Eventually(t, func() bool { return trackReceived.Load() }, 5*time.Second, 100*time.Millisecond)

	pub.Disconnect()
	sub.Disconnect()
}

// This test case can't pass with CI's environment (docker + embedded turn), will be skipped with CI running
func TestForceTLS(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping in CI environment")
	}
	var reconnected atomic.Bool
	pubCB := &RoomCallback{
		OnReconnected: func() {
			reconnected.Store(true)
		},
	}
	pub, err := createAgent(t.Name()+"-"+strconv.Itoa(int(rand.Uint32())), pubCB, "publisher-forcetls")
	require.NoError(t, err)

	// ensure publisher connected
	pub.LocalParticipant.PublishData([]byte("test"), livekit.DataPacket_RELIABLE, nil)

	pub.Simulate(SimulateForceTLS)
	require.Eventually(t, func() bool { return reconnected.Load() && pub.engine.ensurePublisherConnected(true) == nil }, 15*time.Second, 100*time.Millisecond)

	logger.Infow("reconnected")

	getSelectedPair := func(pc *webrtc.PeerConnection) (*webrtc.ICECandidatePair, error) {
		sctp := pc.SCTP()
		if sctp == nil {
			return nil, errors.New("no SCTP")
		}

		dtlsTransport := sctp.Transport()
		if dtlsTransport == nil {
			return nil, errors.New("no DTLS transport")
		}

		iceTransport := dtlsTransport.ICETransport()
		if iceTransport == nil {
			return nil, errors.New("no ICE transport")
		}

		return iceTransport.GetSelectedCandidatePair()
	}

	for _, pc := range []*webrtc.PeerConnection{pub.engine.publisher.pc, pub.engine.subscriber.pc} {
		pair, err := getSelectedPair(pc)
		require.NoError(t, err)
		require.NotNil(t, pair)
		require.Equal(t, pair.Local.Typ, webrtc.ICECandidateTypeRelay)
	}

	pub.Disconnect()
}

func TestSubscribeMutedTrack(t *testing.T) {
	pub, err := createAgent(t.Name(), nil, "publisher")
	require.NoError(t, err)

	videoTrackName := "video_of_pub1"
	var trackLock sync.Mutex
	var trackReceived atomic.Bool

	var pubTrackMuted sync.WaitGroup
	pubTrackMuted.Add(1)

	pubMuteTrack := func(t *testing.T, room *Room, name string) *LocalTrackPublication {
		track, err := NewLocalSampleTrack(webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeVP8,
			ClockRate: 90000,
		})
		require.NoError(t, err)

		track.OnBind(func() {
			go func() {
				defer pubTrackMuted.Done()
				for i := 0; i < 10; i++ {
					time.Sleep(50 * time.Millisecond)
					track.WriteSample(media.Sample{Data: []byte("test"), Duration: 50 * time.Millisecond}, nil)
				}
			}()
		})

		localPub, err := room.LocalParticipant.PublishTrack(track, &TrackPublicationOptions{
			Name: name,
		})
		require.NoError(t, err)
		return localPub
	}

	localPub := pubMuteTrack(t, pub, videoTrackName)
	require.Equal(t, localPub.Name(), videoTrackName)

	localPub.SetMuted(true)
	pubTrackMuted.Wait()

	subCB := &RoomCallback{
		ParticipantCallback: ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant) {
				trackLock.Lock()
				trackReceived.Store(true)
				require.Equal(t, rp.Name(), pub.LocalParticipant.Name())
				require.Equal(t, publication.Name(), videoTrackName)
				require.Equal(t, track.Kind(), webrtc.RTPCodecTypeVideo)
				require.True(t, publication.IsMuted())
				trackLock.Unlock()
			},
		},
	}
	sub, err := createAgent(t.Name(), subCB, "subscriber")
	require.NoError(t, err)
	serverInfo := sub.ServerInfo()
	require.NotNil(t, serverInfo)
	require.Equal(t, serverInfo.Edition, livekit.ServerInfo_Standard)
	require.Eventually(t, func() bool { return trackReceived.Load() }, 5*time.Second, 100*time.Millisecond)

	pub.Disconnect()
	sub.Disconnect()
}

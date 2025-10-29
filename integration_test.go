// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lksdk

import (
	"context"
	"errors"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"

	"github.com/livekit/protocol/livekit"

	"github.com/livekit/server-sdk-go/v2/pkg/interceptor"
)

// The integration test of the SDK. can't run this test standalone, should be run with `mage test`

const (
	host = "ws://localhost:7880"
)

var (
	apiKey, apiSecret string

	VP8KeyFrame8x8 = []byte{
		0x10, 0x02, 0x00, 0x9d, 0x01, 0x2a, 0x08, 0x00,
		0x08, 0x00, 0x00, 0x47, 0x08, 0x85, 0x85, 0x88,
		0x85, 0x84, 0x88, 0x02, 0x02, 0x00, 0x0c, 0x0d,
		0x60, 0x00, 0xfe, 0xff, 0xab, 0x50, 0x80,
	}

	H264KeyFrame2x2SPS = []byte{
		0x67, 0x42, 0xc0, 0x1f, 0x0f, 0xd9, 0x1f, 0x88,
		0x88, 0x84, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00,
		0x00, 0x03, 0x00, 0xc8, 0x3c, 0x60, 0xc9, 0x20,
	}
	H264KeyFrame2x2PPS = []byte{
		0x68, 0x87, 0xcb, 0x83, 0xcb, 0x20,
	}
	H264KeyFrame2x2IDR = []byte{
		0x65, 0x88, 0x84, 0x0a, 0xf2, 0x62, 0x80, 0x00,
		0xa7, 0xbe,
	}
	H264KeyFrame2x2 = [][]byte{H264KeyFrame2x2SPS, H264KeyFrame2x2PPS, H264KeyFrame2x2IDR}
)

func TestMain(m *testing.M) {
	keys := strings.Split(os.Getenv("LIVEKIT_KEYS"), ": ")
	if len(keys) >= 2 {
		apiKey, apiSecret = keys[0], keys[1]
	}

	os.Exit(m.Run())
}

func createAgent(roomName string, callback *RoomCallback, name string, connectOpts ...ConnectOption) (*Room, error) {
	room, err := ConnectToRoom(host, ConnectInfo{
		APIKey:              apiKey,
		APISecret:           apiSecret,
		RoomName:            roomName,
		ParticipantIdentity: name,
	}, callback, connectOpts...)
	if err != nil {
		return nil, err
	}
	return room, nil
}

type SampleTestProvider struct {
	BaseSampleProvider
	SampleDuration time.Duration
	Codec          webrtc.RTPCodecCapability
}

func NewSampleTestProvider(codec webrtc.RTPCodecCapability) *SampleTestProvider {
	sampleDuration := 50 * time.Millisecond
	if strings.Contains(codec.MimeType, "video/") {
		sampleDuration = time.Second / 10
	}
	return &SampleTestProvider{
		SampleDuration: sampleDuration,
		Codec:          codec,
	}
}

func (p *SampleTestProvider) NextSample(ctx context.Context) (media.Sample, error) {
	payload := make([]byte, 512)
	switch {
	case strings.Contains(p.Codec.MimeType, "VP8"):
		payload = payload[:len(VP8KeyFrame8x8)]
		copy(payload, VP8KeyFrame8x8)
	case strings.Contains(p.Codec.MimeType, "H264"):
		payload = payload[:0]
		for _, nalu := range H264KeyFrame2x2 {
			// Annex B prefix
			payload = append(payload, 0x00, 0x00, 0x00, 0x01)
			payload = append(payload, nalu...)
		}
	default:
	}
	return media.Sample{
		Data:     payload,
		Duration: p.SampleDuration,
	}, nil
}

func pubNullTrack(t *testing.T, room *Room, name string, codecs ...webrtc.RTPCodecCapability) *LocalTrackPublication {
	track, err := NewLocalTrack(codecs[0])
	require.NoError(t, err)
	provider := NewSampleTestProvider(codecs[0])

	track.OnBind(func() {
		if err := track.StartWrite(provider, func() {}); err != nil {
			track.log.Errorw("Could not start writing", err)
		}
	})

	var opts []LocalTrackPublishOption
	if len(codecs) > 1 {
		backupTrack, err := NewLocalTrack(codecs[1])
		require.NoError(t, err)

		backupProvider := NewSampleTestProvider(codecs[1])

		backupTrack.OnBind(func() {
			if err := backupTrack.StartWrite(backupProvider, func() {}); err != nil {
				backupTrack.log.Errorw("Could not start writing", err)
			}
		})

		opts = append(opts, WithBackupCodec(backupTrack))
	}
	localPub, err := room.LocalParticipant.PublishTrack(track, &TrackPublicationOptions{
		Name:              name,
		BackupCodecPolicy: livekit.BackupCodecPolicy_SIMULCAST,
	}, opts...)
	require.NoError(t, err)
	return localPub
}

func TestJoin(t *testing.T) {
	remoteParticipantsOfPub := make(chan string, 1)
	pub, err := createAgent(t.Name(), &RoomCallback{
		OnParticipantConnected: func(participant *RemoteParticipant) {
			remoteParticipantsOfPub <- participant.Identity()
		},
	}, "publisher")
	require.NoError(t, err)

	var (
		dataLock     sync.Mutex
		receivedData string
	)

	audioTrackName := "audio_of_pub1"
	var trackLock sync.Mutex
	var trackReceived atomic.Bool

	subCB := &RoomCallback{
		ParticipantCallback: ParticipantCallback{
			OnDataPacket: func(data DataPacket, params DataReceiveParams) {
				switch data := data.(type) {
				case *UserDataPacket:
					dataLock.Lock()
					receivedData = string(data.Payload)
					dataLock.Unlock()
				}
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
	require.Equal(t, livekit.ServerInfo_Standard, serverInfo.Edition)
	require.Equal(t, ConnectionStateConnected, sub.ConnectionState())
	remotParticipant := <-remoteParticipantsOfPub
	require.Equal(t, remotParticipant, sub.LocalParticipant.Identity())

	pub.LocalParticipant.PublishDataPacket(UserData([]byte("test")), WithDataPublishReliable(true))
	pub.LocalParticipant.PublishDataPacket(&livekit.SipDTMF{Digit: "#"}, WithDataPublishReliable(true))
	localPub := pubNullTrack(t, pub, audioTrackName, webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	})

	require.Equal(t, localPub.Name(), audioTrackName)

	require.Eventually(t, func() bool { return trackReceived.Load() }, 5*time.Second, 100*time.Millisecond)
	require.Eventually(t, func() bool {
		dataLock.Lock()
		defer dataLock.Unlock()
		return receivedData == "test"
	}, 5*time.Second, 100*time.Millisecond)

	pub.Disconnect()
	sub.Disconnect()
	require.Equal(t, ConnectionStateDisconnected, sub.ConnectionState())
}

func TestJoinError(t *testing.T) {
	_, err := ConnectToRoomWithToken(host, "invalid", nil)
	require.Error(t, err)

	errString := err.Error()
	require.Contains(t, errString, "unauthorized:")
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
	var trackReceived, subReconnected atomic.Bool
	var subTrack *webrtc.TrackRemote
	subCB := &RoomCallback{
		ParticipantCallback: ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant) {
				trackLock.Lock()
				trackReceived.Store(true)
				require.Equal(t, rp.Name(), pub.LocalParticipant.Name())
				require.Equal(t, publication.Name(), audioTrackName)
				require.Equal(t, track.Kind(), webrtc.RTPCodecTypeAudio)
				subTrack = track
				trackLock.Unlock()
			},
		},
		OnReconnected: func() {
			subReconnected.Store(true)
		},
	}
	sub, err := createAgent(t.Name(), subCB, "subscriber")
	require.NoError(t, err)

	subCB.OnReconnecting = func() {
		require.Equal(t, ConnectionStateReconnecting, sub.ConnectionState())
	}
	subCB.OnReconnected = func() {
		require.Equal(t, ConnectionStateConnected, sub.ConnectionState())
	}

	localPub := pubNullTrack(t, pub, audioTrackName, webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
		Channels:  2,
	})
	require.Equal(t, localPub.Name(), audioTrackName)
	require.Eventually(t, func() bool { return trackReceived.Load() }, 5*time.Second, 100*time.Millisecond)

	pub.Simulate(SimulateSignalReconnect)
	require.Eventually(t, func() bool { return reconnected.Load() }, 5*time.Second, 100*time.Millisecond)
	pub.log.Infow("reconnected")

	// consume track packet
	consumeCh := make(chan struct{})
	var exitConsume atomic.Bool
	go func() {
		trackLock.Lock()
		track := subTrack
		trackLock.Unlock()
		buf := make([]byte, 1024)
		defer close(consumeCh)
		for !exitConsume.Load() {
			_, _, err = track.Read(buf)
			require.NoError(t, err)
		}
	}()

	sub.Simulate(SimulateSignalReconnect)
	require.Eventually(t, func() bool { return subReconnected.Load() }, 5*time.Second, 100*time.Millisecond)

	// confirm track is still working after resume
	exitConsume.Store(true)
	<-consumeCh
	trackLock.Lock()
	track := subTrack
	trackLock.Unlock()
	buf := make([]byte, 1024)
	for i := 0; i < 10; i++ {
		_, _, err = track.Read(buf)
		require.NoError(t, err)
	}

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
	pub.LocalParticipant.PublishDataPacket(UserData([]byte("test")), WithDataPublishReliable(true))

	pub.Simulate(SimulateForceTLS)
	require.Eventually(t, func() bool { return reconnected.Load() && pub.engine.ensurePublisherConnected(true) == nil }, 15*time.Second, 100*time.Millisecond)

	pub.log.Infow("reconnected")

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
	audioTrackName := "audio_of_pub1"
	var trackLock sync.Mutex
	var trackReceived atomic.Int32

	var pubTrackMuted sync.WaitGroup
	require.NoError(t, pub.LocalParticipant.PublishDataPacket(UserData([]byte("test"))), WithDataPublishReliable(true))

	pubMuteTrack := func(t *testing.T, room *Room, name string, codec webrtc.RTPCodecCapability) *LocalTrackPublication {
		pubTrackMuted.Add(1)
		track, err := NewLocalTrack(codec)
		require.NoError(t, err)

		track.OnBind(func() {
			go func() {
				defer pubTrackMuted.Done()
				for i := 0; i < 10; i++ {
					time.Sleep(50 * time.Millisecond)
					require.NoError(t, track.WriteSample(media.Sample{Data: []byte("test"), Duration: 50 * time.Millisecond}, nil))
				}
			}()
		})

		localPub, err := room.LocalParticipant.PublishTrack(track, &TrackPublicationOptions{
			Name: name,
		})
		require.NoError(t, err)
		return localPub
	}

	localVideoPub := pubMuteTrack(t, pub, videoTrackName, webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeVP8,
		ClockRate: 90000,
	})
	require.Equal(t, localVideoPub.Name(), videoTrackName)

	localAudioPub := pubMuteTrack(t, pub, audioTrackName, webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeOpus,
		ClockRate: 48000,
	})
	require.Equal(t, localAudioPub.Name(), audioTrackName)

	localVideoPub.SetMuted(true)
	localAudioPub.SetMuted(true)
	pubTrackMuted.Wait()

	subCB := &RoomCallback{
		ParticipantCallback: ParticipantCallback{
			OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant) {
				trackLock.Lock()
				trackReceived.Inc()
				require.Equal(t, rp.Name(), pub.LocalParticipant.Name())
				if track.Kind() == webrtc.RTPCodecTypeAudio {
					require.Equal(t, publication.Name(), audioTrackName)
				} else {
					require.Equal(t, publication.Name(), videoTrackName)
				}
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
	require.Eventually(t, func() bool { return trackReceived.Load() == 2 }, 5*time.Second, 100*time.Millisecond)

	pub.Disconnect()
	sub.Disconnect()
}

func TestLimitPayloadSize(t *testing.T) {
	pub, err := createAgent(t.Name(), nil, "publisher")
	require.NoError(t, err)

	videoTrackName := "video_of_pub1"

	localTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:  webrtc.MimeTypeVP8,
		ClockRate: 90000,
	}, videoTrackName, videoTrackName)
	require.NoError(t, err)

	_, err = pub.LocalParticipant.PublishTrack(localTrack, &TrackPublicationOptions{Name: videoTrackName})
	require.NoError(t, err)

	// wait for track to be published
	time.Sleep(500 * time.Millisecond)

	rtpPkt := rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: 1,
			Timestamp:      1,
		},
		Payload: make([]byte, interceptor.MaxPayloadSize),
	}
	require.NoError(t, localTrack.WriteRTP(&rtpPkt))
	rtpPkt.SequenceNumber++
	rtpPkt.Payload = make([]byte, interceptor.MaxPayloadSize+1)
	require.ErrorIs(t, localTrack.WriteRTP(&rtpPkt), interceptor.ErrPayloadSizeTooLarge)

	pub.Disconnect()
}

func TestSimulcastCodec(t *testing.T) {
	cases := []struct {
		name                      string
		primaryCodec, backupCodec webrtc.RTPCodecCapability
	}{
		{
			name: "video",
			primaryCodec: webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypeVP8,
				ClockRate: 90000,
			},
			backupCodec: webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypeH264,
				ClockRate: 90000,
			},
		},
		{
			name: "audio",
			primaryCodec: webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypePCMU,
				ClockRate: 8000,
				Channels:  1,
			},
			backupCodec: webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypeOpus,
				ClockRate: 48000,
				Channels:  2,
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pub, err := createAgent(t.Name(), nil, "publisher")
			require.NoError(t, err)
			trackPub := pubNullTrack(t, pub, "simulcast_codec", []webrtc.RTPCodecCapability{c.primaryCodec, c.backupCodec}...)
			t.Log("published track with simulcast codecs:", trackPub.SID())

			var subWG sync.WaitGroup
			subWG.Add(2) // primary and backup
			primarySub, err := createAgent(t.Name(), &RoomCallback{
				ParticipantCallback: ParticipantCallback{
					OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant) {
						subWG.Done()
						t.Log("primary codec subscribed:", track.Codec().MimeType, publication.SID())
						require.Equal(t, publication.SID(), trackPub.SID())
						require.Equal(t, c.primaryCodec.MimeType, track.Codec().MimeType)
					},
				},
			}, "primary_subscriber")
			require.NoError(t, err)

			backupSub, err := createAgent(t.Name(), &RoomCallback{
				ParticipantCallback: ParticipantCallback{
					OnTrackSubscribed: func(track *webrtc.TrackRemote, publication *RemoteTrackPublication, rp *RemoteParticipant) {
						subWG.Done()
						t.Log("backup codec subscribed:", track.Codec().MimeType, publication.SID())
						require.Equal(t, publication.SID(), trackPub.SID())
						require.Equal(t, c.backupCodec.MimeType, track.Codec().MimeType)
					},
				},
			}, "backup_subscriber", withCodecs([]webrtc.RTPCodecParameters{
				{
					RTPCodecCapability: c.backupCodec,
					PayloadType:        96,
				},
			}))
			require.NoError(t, err)

			subWG.Wait()

			pub.Disconnect()
			primarySub.Disconnect()
			backupSub.Disconnect()
		})
	}

}

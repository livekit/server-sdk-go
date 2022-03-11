package trackrecorder

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/livekit/protocol/auth"
	lksdk "github.com/livekit/server-sdk-go"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/stretchr/testify/require"
)

func mockPacket(id uint16, p []byte) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			SequenceNumber: id,
		},
		Payload: p,
	}
}

func promoteRecorder(r Recorder) *recorder {
	rec, ok := r.(*recorder)
	if !ok {
		panic("cannot promote Recorder to *recorder")
	}
	return rec
}

func TestCreateRecorderForVideo(t *testing.T) {
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeVP8,
		},
	}
	sink := NewBufferSink("test")

	tr, err := NewWith(sink, codec)
	require.NoError(t, err)
	require.NotNil(t, tr)

	rec := promoteRecorder(tr)
	require.NotNil(t, rec.sb)
	require.NotNil(t, rec.mw)
}

func TestCreateRecorderForAudio(t *testing.T) {
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus,
			Channels: 2,
		},
	}
	sink := NewBufferSink("test")

	tr, err := NewWith(sink, codec)
	require.NoError(t, err)
	require.NotNil(t, tr)

	rec := promoteRecorder(tr)
	require.NotNil(t, rec.sb)
	require.NotNil(t, rec.mw)
}

func TestFailCreateRecorderForUnsupportedCodec(t *testing.T) {
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeAV1,
		},
	}
	sink := NewBufferSink("test")
	_, err := NewWith(sink, codec)
	require.ErrorIs(t, err, ErrCodecNotSupported)
}

func TestWritePacketsWithSampleBuffer(t *testing.T) {
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			// Choose between VP8, H264 and Opus
			MimeType: webrtc.MimeTypeH264,
			Channels: 2,
		},
	}
	sink := NewBufferSink("test")
	tr, _ := NewWith(sink, codec)
	rec := promoteRecorder(tr)
	require.NotNil(t, rec.sb)

	// Write multiple packets
	for i := 0; i < 10; i++ {
		payload := []byte(fmt.Sprintf("Hello World %d\n!", i))
		packet := mockPacket(uint16(i), payload)
		err := rec.writeToSink(packet)
		require.NoError(t, err)
	}
}

func TestWritePacketsWithoutSampleBuffer(t *testing.T) {
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeVP8,
			Channels: 1,
		},
	}
	sink := NewBufferSink("test")
	tr, _ := NewWith(sink, codec)
	rec := promoteRecorder(tr)

	// Set sample buffer to be nil
	rec.sb = nil

	// Write multiple packets and still expect no errors
	for i := 0; i < 10; i++ {
		payload := []byte(fmt.Sprintf("Hello World %d\n!", i))
		packet := mockPacket(uint16(i), payload)
		err := rec.writeToSink(packet)
		require.NoError(t, err)
	}
}

func TestSinkEquality(t *testing.T) {
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeVP8,
		},
	}
	sink := NewBufferSink("test")
	tr, _ := NewWith(sink, codec)

	// Expect stored sink is the same as passed sink
	require.Equal(t, sink, tr.Sink())
}

func getEnvOrFail(key string) string {
	val := os.Getenv(key)
	if val == "" {
		log.Fatalf("%s not set", key)
	}
	return val
}

func TestRecorderUsageScenario(t *testing.T) {
	url := getEnvOrFail("LIVEKIT_URL")
	apiKey := getEnvOrFail("LIVEKIT_API_KEY")
	apiSecret := getEnvOrFail("LIVEKIT_API_SECRET")
	testRoomName := "livekit-egress-test"
	TRUE := true
	FALSE := false
	participantID := "lk-participant"
	recorderID := "lk-recorder"

	// -----
	// Generate access tokens for participant and recorder
	// -----

	pAT := auth.NewAccessToken(apiKey, apiSecret)
	pGrant := &auth.VideoGrant{
		RoomJoin:     true,
		Room:         testRoomName,
		CanPublish:   &TRUE,
		CanSubscribe: &TRUE,
	}
	pAT.AddGrant(pGrant).SetIdentity(participantID).SetValidFor(time.Hour)
	pToken, err := pAT.ToJWT()
	require.NoError(t, err)

	rAT := auth.NewAccessToken(apiKey, apiSecret)
	rGrant := &auth.VideoGrant{
		RoomJoin:       true,
		Room:           testRoomName,
		CanPublish:     &FALSE,
		CanPublishData: &FALSE,
		CanSubscribe:   &TRUE,
		Hidden:         true,
		Recorder:       true,
	}
	rAT.AddGrant(rGrant).SetIdentity(recorderID).SetValidFor(time.Hour)
	rToken, err := rAT.ToJWT()
	require.NoError(t, err)

	// -----
	// Connect to room
	// -----

	var pRoom, rRoom *lksdk.Room

	pRoom, err = lksdk.ConnectToRoomWithToken(url, pToken)
	require.NoError(t, err)
	defer pRoom.Disconnect()

	rRoom, err = lksdk.ConnectToRoomWithToken(url, rToken)
	require.NoError(t, err)
	defer rRoom.Disconnect()

	// -----
	// Create track for participant to publish
	// -----

	preferredCodec := webrtc.MimeTypeVP8

	sampleTrack, err := lksdk.NewLocalSampleTrack(webrtc.RTPCodecCapability{
		MimeType:  preferredCodec,
		ClockRate: 90000,
		Channels:  1,
	})
	require.NoError(t, err)

	publication, err := pRoom.LocalParticipant.PublishTrack(sampleTrack, &lksdk.TrackPublicationOptions{
		Name: participantID + "-video",
	})
	require.NoError(t, err)

	// -----
	// Publish static media packets until we receive stop signal
	// -----

	sampleProvider := lksdk.NewNullSampleProvider(90000)
	sampleDone := make(chan struct{}, 1)
	go func() {
		var sample media.Sample
		for {
			select {
			case <-sampleDone:
				pRoom.LocalParticipant.UnpublishTrack(publication.SID())
				return
			default:
				sample, err = sampleProvider.NextSample()
				require.NoError(t, err)
				sampleTrack.WriteSample(sample, nil)
			}
		}
	}()

	// -----
	// Create recorder since we know the codec we'll be publishing
	// -----
	ctx := context.Background()
	codec := webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeVP8,
		},
	}
	var rec Recorder
	rec, err = New(participantID+"-video.ivf", codec)
	require.NoError(t, err)
	require.NotNil(t, rec)

	rRoom.Callback.OnTrackSubscribed = func(track *webrtc.TrackRemote, publication *lksdk.RemoteTrackPublication, rp *lksdk.RemoteParticipant) {
		require.NotNil(t, rec)
		rec.Start(ctx, track)
	}

	// -----
	// Let participant publish track for a while,
	// then stop publishing and recording
	// -----
	time.Sleep(time.Second * 5)
	close(sampleDone)

	require.NotNil(t, rec)
	rec.Stop()

	// In real-world scenario, once Stop() is invoked, the main function will keep running.
	// Let's introduce a small delay for the recorder to finish gracefully and improve code coverage
	time.Sleep(time.Second * 1)

	// Remember to remove video file afterwards
	os.Remove(participantID + "-video.ivf")
}

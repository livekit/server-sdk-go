package lksdk

import (
	"math"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/stretchr/testify/require"
)

func TestWriteSampleMaxPrevDroppedPackets(t *testing.T) {
	const clockRate = 48000
	track, err := NewLocalTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: clockRate, Channels: 2})
	require.NoError(t, err)
	track.sequencer = rtp.NewRandomSequencer()
	track.packetizer = rtp.NewPacketizer(rtpOutboundMTU, 0, 0, &codecs.OpusPayloader{}, track.sequencer, clockRate)
	track.clockRate = clockRate

	sample := media.Sample{Data: []byte{0}, Duration: 20 * time.Millisecond}
	require.NoError(t, track.WriteSample(sample, nil))
	before := track.lastRTPTimestamp

	sample.PrevDroppedPackets = math.MaxUint16
	require.NoError(t, track.WriteSample(sample, nil))
	samplesPerFrame := uint32(sample.Duration.Seconds() * clockRate)
	require.Equal(t, (math.MaxUint16+1)*samplesPerFrame, track.lastRTPTimestamp-before)
}

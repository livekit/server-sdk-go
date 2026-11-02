package lksdk

import (
	"context"
	"io"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/interceptor"
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

func TestWriteCompleteNotCalledOnRebind(t *testing.T) {
	track := newOpusTestTrack(t)
	provider := newIdleProvider()
	var completed atomic.Int32
	require.NoError(t, track.StartWrite(provider, func() { completed.Add(1) }))

	// a full reconnect unbinds the track from the old PeerConnection and binds it to a new one
	bindTrack(t, track)
	require.Eventually(t, func() bool { return provider.reads.Load() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, track.Unbind(&fakeTrackLocalContext{}))
	bindTrack(t, track)
	require.Eventually(t, func() bool { return provider.reads.Load() == 2 }, time.Second, time.Millisecond)
	require.Never(t, func() bool { return completed.Load() != 0 }, 200*time.Millisecond, 10*time.Millisecond)

	// the provider ending still completes, once
	close(provider.eof)
	require.Eventually(t, func() bool { return completed.Load() == 1 }, time.Second, time.Millisecond)
	require.NoError(t, track.Close())
	require.Never(t, func() bool { return completed.Load() != 1 }, 100*time.Millisecond, 10*time.Millisecond)
}

func TestWriteCompleteCalledAfterWriterStops(t *testing.T) {
	for name, stop := range map[string]func(*LocalTrack){
		"replaced": func(track *LocalTrack) {
			require.NoError(t, track.StartWrite(newIdleProvider(), nil))
		},
		"replaced while unbound": func(track *LocalTrack) {
			require.NoError(t, track.Unbind(&fakeTrackLocalContext{}))
			require.NoError(t, track.StartWrite(newIdleProvider(), nil))
		},
		"closed": func(track *LocalTrack) {
			require.NoError(t, track.Close())
		},
		"closed while unbound": func(track *LocalTrack) {
			require.NoError(t, track.Unbind(&fakeTrackLocalContext{}))
			require.NoError(t, track.Close())
		},
	} {
		t.Run(name, func(t *testing.T) {
			track := newOpusTestTrack(t)
			bindTrack(t, track)

			// the writer takes a moment to notice it was stopped
			provider := newIdleProvider()
			provider.stopDelay = 100 * time.Millisecond
			readingAtComplete := make(chan int32, 2)
			require.NoError(t, track.StartWrite(provider, func() { readingAtComplete <- provider.reading.Load() }))
			require.Eventually(t, func() bool { return provider.reads.Load() == 1 }, time.Second, time.Millisecond)

			stop(track)
			select {
			case reading := <-readingAtComplete:
				require.Zero(t, reading, "onComplete ran while the writer was still reading")
			case <-time.After(time.Second):
				require.Fail(t, "onComplete not called")
			}
			require.NoError(t, track.Close())
			require.Never(t, func() bool { return len(readingAtComplete) != 0 }, 100*time.Millisecond, 10*time.Millisecond)
		})
	}
}

func TestWriterNotStartedAfterUnbind(t *testing.T) {
	track := newOpusTestTrack(t)
	provider := newIdleProvider()
	var completed atomic.Int32
	require.NoError(t, track.StartWrite(provider, func() { completed.Add(1) }))

	// unbind before the writer started by Bind gets going
	track.writeStartupLock.Lock()
	bindTrack(t, track)
	require.NoError(t, track.Unbind(&fakeTrackLocalContext{}))
	track.writeStartupLock.Unlock()
	require.Never(t, func() bool { return provider.reads.Load() != 0 }, 100*time.Millisecond, 10*time.Millisecond)

	require.NoError(t, track.StartWrite(newIdleProvider(), nil))
	require.Eventually(t, func() bool { return completed.Load() == 1 }, time.Second, time.Millisecond)
}

func TestWriterNotStartedAfterClose(t *testing.T) {
	track := newOpusTestTrack(t)
	bindTrack(t, track)
	provider := newIdleProvider()
	var completed atomic.Int32
	require.NoError(t, track.StartWrite(provider, func() { completed.Add(1) }))

	// close before the writer started by StartWrite gets going
	track.writeStartupLock.Lock()
	require.NoError(t, track.Close())
	track.writeStartupLock.Unlock()
	require.Eventually(t, func() bool { return completed.Load() == 1 }, time.Second, time.Millisecond)
	require.Never(t, func() bool { return provider.reads.Load() != 0 }, 100*time.Millisecond, 10*time.Millisecond)
}

func newOpusTestTrack(t *testing.T) *LocalTrack {
	track, err := NewLocalTrack(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2})
	require.NoError(t, err)
	t.Cleanup(func() { _ = track.Close() })
	return track
}

func bindTrack(t *testing.T, track *LocalTrack) {
	_, err := track.Bind(&fakeTrackLocalContext{})
	require.NoError(t, err)
}

// idleProvider has no samples until eof is closed, like a live source
type idleProvider struct {
	eof       chan struct{}
	stopDelay time.Duration
	reads     atomic.Int32
	reading   atomic.Int32
}

func newIdleProvider() *idleProvider {
	return &idleProvider{eof: make(chan struct{})}
}

func (p *idleProvider) NextSample(ctx context.Context) (media.Sample, error) {
	p.reads.Add(1)
	p.reading.Add(1)
	defer p.reading.Add(-1)
	select {
	case <-ctx.Done():
		time.Sleep(p.stopDelay)
		return media.Sample{}, ctx.Err()
	case <-p.eof:
		return media.Sample{}, io.EOF
	}
}

func (p *idleProvider) OnBind() error   { return nil }
func (p *idleProvider) OnUnbind() error { return nil }
func (p *idleProvider) Close() error    { return nil }

type fakeTrackLocalContext struct{}

func (c *fakeTrackLocalContext) CodecParameters() []webrtc.RTPCodecParameters {
	return []webrtc.RTPCodecParameters{{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2},
		PayloadType:        111,
	}}
}

func (c *fakeTrackLocalContext) HeaderExtensions() []webrtc.RTPHeaderExtensionParameter { return nil }
func (c *fakeTrackLocalContext) SSRC() webrtc.SSRC                                      { return 1 }
func (c *fakeTrackLocalContext) SSRCRetransmission() webrtc.SSRC                        { return 0 }
func (c *fakeTrackLocalContext) SSRCForwardErrorCorrection() webrtc.SSRC                { return 0 }
func (c *fakeTrackLocalContext) WriteStream() webrtc.TrackLocalWriter                   { return nil }
func (c *fakeTrackLocalContext) ID() string                                             { return "pc" }

func (c *fakeTrackLocalContext) RTCPReader() interceptor.RTCPReader {
	return interceptor.RTCPReaderFunc(func([]byte, interceptor.Attributes) (int, interceptor.Attributes, error) {
		return 0, nil, io.EOF
	})
}

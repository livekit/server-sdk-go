package media

import (
	"fmt"
	"math"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/opus"
	protoLogger "github.com/livekit/protocol/logger"
)

const (
	// Only reached when an expected event never happens.
	hangGuard = 5 * time.Second

	packetTicks     = DefaultOpusSampleRate / int(time.Second/defaultPCMFrameDuration)
	framesPerSecond = int(time.Second / defaultPCMFrameDuration)
	// Packets collected past the audio so resampler and codec tails are out.
	tailPackets = 10
	// Packets after the last audio frame that still decode with energy: opus look-ahead, then decoder ringing.
	codecTailPackets = 3

	toneAmp    = math.MaxInt16 / 2
	toneEdge   = toneAmp / 4
	silentPeak = 100
)

var sourceRates = []int{8000, 16000, 22050, 44100, 48000}

func tones(rate, frames int, amp float64, hz ...float64) media.PCM16Sample {
	out := make(media.PCM16Sample, 0, frames*len(hz))
	for i := range frames {
		for _, f := range hz {
			out = append(out, int16(amp*math.Sin(2*math.Pi*f*float64(i)/float64(rate))))
		}
	}
	return out
}

func channelHz(channels int) []float64 {
	return []float64{leftHz, rightHz}[:channels]
}

// Best correlation of channel c with a sine of hz over one period of phase.
func chanCorr(pcm media.PCM16Sample, channels, c int, hz float64, rate int) float64 {
	var x []float64
	for i := c; i < len(pcm); i += channels {
		x = append(x, float64(pcm[i]))
	}
	best := 0.0
	for lag := range int(float64(rate)/hz) + 1 {
		var sxy, sxx, syy float64
		for i := lag; i < len(x); i++ {
			y := math.Sin(2 * math.Pi * hz * float64(i-lag) / float64(rate))
			sxy, sxx, syy = sxy+x[i]*y, sxx+x[i]*x[i], syy+y*y
		}
		if sxx > 0 {
			best = max(best, sxy/math.Sqrt(sxx*syy))
		}
	}
	return best
}

// Frames of channel c from the first to past the last sample louder than toneEdge.
func toneSpan(pcm media.PCM16Sample, channels, c int) (start, end int) {
	start = -1
	for i := c; i < len(pcm); i += channels {
		if math.Abs(float64(pcm[i])) >= toneEdge {
			if start < 0 {
				start = i / channels
			}
			end = i/channels + 1
		}
	}
	return max(start, 0), end
}

// The tone minus 20 ms at each end, where resampler and codec edges sit.
func toneBody(t *testing.T, pcm media.PCM16Sample, channels, rate int) media.PCM16Sample {
	t.Helper()
	start, end := toneSpan(pcm, channels, 0)
	margin := rate / 50
	require.Greater(t, end-start, 4*margin, "the tone is missing")
	return pcm[(start+margin)*channels : (end-margin)*channels]
}

func requireTones(t *testing.T, pcm media.PCM16Sample, channels, rate int, hz ...float64) {
	t.Helper()
	seg := toneBody(t, pcm, channels, rate)
	for c, f := range hz {
		require.Greater(t, chanCorr(seg, channels, c, f, rate), 0.95, "channel %d", c)
	}
}

type sentPacket struct {
	seq     uint16
	ts      uint32
	payload []byte
}

type hold struct {
	reached, released chan struct{}
	once              sync.Once
}

func (h *hold) release() { h.once.Do(func() { close(h.released) }) }

// Stands in for the PeerConnection's write stream. A hold stops the track's pacing loop inside WriteRTP at that
// packet index until released, so writes land at a known frame.
type rtpSink struct {
	mu    sync.Mutex
	pkts  []sentPacket
	grew  chan struct{}
	holds map[int]*hold
}

func (s *rtpSink) WriteRTP(hdr *rtp.Header, payload []byte) (int, error) {
	s.mu.Lock()
	h := s.holds[len(s.pkts)]
	s.mu.Unlock()
	if h != nil {
		close(h.reached)
		<-h.released
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pkts = append(s.pkts, sentPacket{hdr.SequenceNumber, hdr.Timestamp, slices.Clone(payload)})
	close(s.grew)
	s.grew = make(chan struct{})
	return len(payload), nil
}

func (s *rtpSink) Write(b []byte) (int, error) { return len(b), nil }

func (s *rtpSink) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pkts)
}

// Returns the packets sent once done accepts them, or false if it never does.
func (s *rtpSink) until(done func([]sentPacket) bool) ([]sentPacket, bool) {
	timeout := time.After(hangGuard)
	for {
		s.mu.Lock()
		pkts, grew := slices.Clone(s.pkts), s.grew
		s.mu.Unlock()
		if done(pkts) {
			return pkts, true
		}
		select {
		case <-grew:
		case <-timeout:
			return pkts, false
		}
	}
}

func (s *rtpSink) wait(t *testing.T, n int) []sentPacket {
	t.Helper()
	pkts, ok := s.until(func(pkts []sentPacket) bool { return len(pkts) >= n })
	if !ok {
		t.Fatalf("only %d of %d packets were sent", len(pkts), n)
	}
	return pkts[:n]
}

func (s *rtpSink) reached(t *testing.T, n int) {
	t.Helper()
	select {
	case <-s.holds[n].reached:
	case <-time.After(hangGuard):
		t.Fatalf("the track never sent packet %d", n)
	}
}

func (s *rtpSink) release(n int) { s.holds[n].release() }

type fakeTrackContext struct{ sink *rtpSink }

func (c fakeTrackContext) CodecParameters() []webrtc.RTPCodecParameters {
	return []webrtc.RTPCodecParameters{{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: DefaultOpusSampleRate, Channels: 2},
		PayloadType:        111,
	}}
}
func (fakeTrackContext) HeaderExtensions() []webrtc.RTPHeaderExtensionParameter { return nil }
func (fakeTrackContext) SSRC() webrtc.SSRC                                      { return 1 }
func (fakeTrackContext) SSRCRetransmission() webrtc.SSRC                        { return 0 }
func (fakeTrackContext) SSRCForwardErrorCorrection() webrtc.SSRC                { return 0 }
func (c fakeTrackContext) WriteStream() webrtc.TrackLocalWriter                 { return c.sink }
func (fakeTrackContext) ID() string                                             { return "fake" }
func (fakeTrackContext) RTCPReader() interceptor.RTCPReader                     { return nil }

// Packet 0 is the silence frame the track sends on Bind. Holding it lets a test queue audio before the first
// audio frame is cut.
// Homebrew's libsoxr can crash when soxr_create runs concurrently (the Debian builds CI uses do not), and creating
// a track creates its resampler, so parallel tests build tracks one at a time.
var trackBuild sync.Mutex

func newLocalTrack(t *testing.T, rate, channels int) *PCMLocalTrack {
	t.Helper()
	trackBuild.Lock()
	defer trackBuild.Unlock()
	track, err := NewPCMLocalTrack(rate, channels, protoLogger.GetLogger())
	require.NoError(t, err)
	return track
}

func newRemoteTrack(t *testing.T, remote *webrtc.TrackRemote, w PCMRemoteTrackWriter, opts ...PCMRemoteTrackOption) *PCMRemoteTrack {
	t.Helper()
	trackBuild.Lock()
	defer trackBuild.Unlock()
	track, err := NewPCMRemoteTrack(remote, w, opts...)
	require.NoError(t, err)
	return track
}

func newBoundTrack(t *testing.T, rate, channels int, holdAt ...int) (*PCMLocalTrack, *rtpSink) {
	t.Helper()
	track := newLocalTrack(t, rate, channels)
	sink := &rtpSink{grew: make(chan struct{}), holds: map[int]*hold{}}
	for _, n := range holdAt {
		sink.holds[n] = &hold{reached: make(chan struct{}), released: make(chan struct{})}
	}
	t.Cleanup(func() {
		for _, h := range sink.holds {
			h.release()
		}
		_ = track.Close()
	})
	_, err := track.Bind(fakeTrackContext{sink})
	require.NoError(t, err)
	return track, sink
}

func decodePackets(t *testing.T, pkts []sentPacket, channels int) media.PCM16Sample {
	t.Helper()
	var out media.PCM16Sample
	dec, err := opus.Decode(media.NewPCM16BufferWriter(&out, DefaultOpusSampleRate), channels, protoLogger.GetLogger())
	require.NoError(t, err)
	for _, p := range pkts {
		require.NoError(t, dec.WriteSample(p.payload))
	}
	return out
}

func packetPeaks(pcm media.PCM16Sample, channels int) []int {
	var peaks []int
	for p := range slices.Chunk(pcm, packetTicks*channels) {
		peak := 0
		for _, v := range p {
			peak = max(peak, int(math.Abs(float64(v))))
		}
		peaks = append(peaks, peak)
	}
	return peaks
}

func requireContinuous(t *testing.T, pkts []sentPacket) {
	t.Helper()
	for i := 1; i < len(pkts); i++ {
		require.Equal(t, uint16(1), pkts[i].seq-pkts[i-1].seq, "sequence gap before packet %d", i)
		require.Equal(t, uint32(packetTicks), pkts[i].ts-pkts[i-1].ts, "timestamp step before packet %d", i)
	}
}

func requireLoud(t *testing.T, peaks []int, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		require.Greater(t, peaks[i], toneAmp/2, "packet %d should carry the tone", i)
	}
}

func requireSilent(t *testing.T, peaks []int, from, to int) {
	t.Helper()
	for i := from; i < to; i++ {
		require.Less(t, peaks[i], silentPeak, "packet %d should be silent", i)
	}
}

func TestLocalTrackKeepsTonesAndChannels(t *testing.T) {
	const frames = 30
	for _, rate := range sourceRates {
		for _, ch := range []int{1, 2} {
			t.Run(fmt.Sprintf("%d/channels=%d", rate, ch), func(t *testing.T) {
				t.Parallel()
				hz := channelHz(ch)
				track, sink := newBoundTrack(t, rate, ch, 0)
				sink.reached(t, 0)
				require.NoError(t, track.WriteSample(tones(rate, rate*frames/framesPerSecond, toneAmp, hz...)))
				sink.release(0)
				out := decodePackets(t, sink.wait(t, 1+frames+tailPackets), ch)
				requireTones(t, out, ch, DefaultOpusSampleRate, hz...)
			})
		}
	}
}

// Each packet is one 10 ms opus frame, so a second of input must fill a second of packets at any source rate.
func TestLocalTrackPacesOneFramePerPacket(t *testing.T) {
	const seconds = 1
	for _, rate := range sourceRates {
		for _, ch := range []int{1, 2} {
			t.Run(fmt.Sprintf("%d/channels=%d", rate, ch), func(t *testing.T) {
				t.Parallel()
				track, sink := newBoundTrack(t, rate, ch, 0)
				sink.reached(t, 0)
				require.NoError(t, track.WriteSample(tones(rate, seconds*rate, toneAmp, channelHz(ch)...)))
				sink.release(0)
				pkts := sink.wait(t, 1+seconds*framesPerSecond+tailPackets)
				requireContinuous(t, pkts)
				start, end := toneSpan(decodePackets(t, pkts, ch), ch, 0)
				require.InDelta(t, seconds*DefaultOpusSampleRate, end-start, float64(packetTicks)/2, "audio duration")
			})
		}
	}
}

// The track re-frames its queue every 10 ms, so how the caller sizes writes must not change what is published.
// At 48 kHz no resampler runs, so payloads compare exactly; soxr output differs from run to run.
func TestLocalTrackWriteSizesDoNotChangeAudio(t *testing.T) {
	const rate, frames = DefaultOpusSampleRate, 20
	for _, ch := range []int{1, 2} {
		src := tones(rate, rate*frames/framesPerSecond, toneAmp, channelHz(ch)...)
		publish := func(t *testing.T, size ...int) [][]byte {
			track, sink := newBoundTrack(t, rate, ch, 0)
			sink.reached(t, 0)
			for i, rest := 0, src; len(rest) > 0; i++ {
				n := min(size[i%len(size)], len(rest))
				require.NoError(t, track.WriteSample(rest[:n]))
				rest = rest[n:]
			}
			sink.release(0)
			var payloads [][]byte
			for _, p := range sink.wait(t, 1+frames+tailPackets) {
				payloads = append(payloads, p.payload)
			}
			return payloads
		}
		cases := []struct {
			name string
			size []int
		}{
			{"one sample", []int{1}},
			{"odd sizes", []int{1, 7, 331, 997}},
			{"7 ms", []int{rate * 7 / 1000 * ch}},
			{"one block", []int{len(src)}},
		}
		for _, c := range cases {
			t.Run(fmt.Sprintf("channels=%d/%s", ch, c.name), func(t *testing.T) {
				t.Parallel()
				require.Equal(t, publish(t, packetTicks*ch), publish(t, c.size...))
			})
		}
	}
}

// Callers use WaitForPlayout to learn their audio went out, e.g. before unpublishing. The transport holds the last
// audio packet, so WaitForPlayout must still be waiting until it is let through.
func TestLocalTrackWaitForPlayoutWaitsForLastFrame(t *testing.T) {
	t.Parallel()
	const frames, last = 10, 10
	const grace = hangGuard / 10
	// Holding the packet after the look-ahead frame keeps the count still while WaitForPlayout returns.
	track, sink := newBoundTrack(t, DefaultOpusSampleRate, 1, 0, last, last+2)
	sink.reached(t, 0)
	require.NoError(t, track.WriteSample(tones(DefaultOpusSampleRate, packetTicks*frames, toneAmp, leftHz)))
	sentAtReturn := make(chan int, 1)
	go func() {
		track.WaitForPlayout()
		sentAtReturn <- sink.len()
	}()
	sink.release(0)
	sink.reached(t, last)

	var sent int
	select {
	case sent = <-sentAtReturn:
	case <-time.After(grace):
		// Still waiting, as it should be: let the last packet through.
		sink.release(last)
		select {
		case sent = <-sentAtReturn:
		case <-time.After(hangGuard):
			t.Fatal("WaitForPlayout never returned")
		}
	}
	// One frame past the last lets the encoder flush its look-ahead.
	require.Equal(t, 2+frames, sent, "packets sent when WaitForPlayout returned")
}

func TestLocalTrackClearQueueLeavesSilence(t *testing.T) {
	t.Parallel()
	const frames, cut = 30, 5
	track, sink := newBoundTrack(t, DefaultOpusSampleRate, 1, 0, cut)
	sink.reached(t, 0)
	require.NoError(t, track.WriteSample(tones(DefaultOpusSampleRate, packetTicks*frames, toneAmp, leftHz)))
	sink.release(0)
	sink.reached(t, cut)
	track.ClearQueue()
	sink.release(cut)

	pkts := sink.wait(t, cut+1+tailPackets)
	requireContinuous(t, pkts)
	peaks := packetPeaks(decodePackets(t, pkts, 1), 1)
	requireLoud(t, peaks, 1, cut+1)
	requireSilent(t, peaks, cut+1+codecTailPackets, len(peaks))
}

// Muting drops what is queued and what is written while muted, but keeps sending silence so timestamps run on.
func TestLocalTrackMuteSendsSilence(t *testing.T) {
	t.Parallel()
	const frames, muteAt, unmuteAt, resumed = 30, 5, 15, 10
	track, sink := newBoundTrack(t, DefaultOpusSampleRate, 1, 0, muteAt, unmuteAt)
	sink.reached(t, 0)
	require.NoError(t, track.WriteSample(tones(DefaultOpusSampleRate, packetTicks*frames, toneAmp, leftHz)))
	sink.release(0)

	sink.reached(t, muteAt)
	require.NoError(t, track.setMuted(true))
	require.NoError(t, track.WriteSample(tones(DefaultOpusSampleRate, packetTicks*frames, toneAmp, leftHz)))
	sink.release(muteAt)

	sink.reached(t, unmuteAt)
	require.NoError(t, track.setMuted(false))
	require.NoError(t, track.WriteSample(tones(DefaultOpusSampleRate, packetTicks*resumed, toneAmp, leftHz)))
	sink.release(unmuteAt)

	pkts := sink.wait(t, unmuteAt+1+resumed+tailPackets)
	requireContinuous(t, pkts)
	peaks := packetPeaks(decodePackets(t, pkts, 1), 1)
	requireLoud(t, peaks, 1, muteAt+1)
	requireSilent(t, peaks, muteAt+1+codecTailPackets, unmuteAt+1)
	requireLoud(t, peaks, unmuteAt+1, unmuteAt+1+resumed)
}

// Close lets the pacing loop drain the queue, so every frame written before Close is still published.
func TestLocalTrackCloseDrainsQueue(t *testing.T) {
	const frames = 20
	for _, rate := range sourceRates {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			t.Parallel()
			track, sink := newBoundTrack(t, rate, 1, 0)
			sink.reached(t, 0)
			require.NoError(t, track.WriteSample(tones(rate, rate*frames/framesPerSecond, toneAmp, leftHz)))
			require.NoError(t, track.Close())
			require.Error(t, track.WriteSample(tones(rate, rate/framesPerSecond, toneAmp, leftHz)), "write after Close")
			sink.release(0)

			// The sine starts and ends below toneSpan's threshold for part of a cycle.
			const edges = DefaultOpusSampleRate / leftHz
			var got int
			_, ok := sink.until(func(pkts []sentPacket) bool {
				start, end := toneSpan(decodePackets(t, pkts, 1), 1, 0)
				got = end - start
				return got >= frames*packetTicks-edges
			})
			require.True(t, ok, "published %d of %d samples written before Close", got, frames*packetTicks)
		})
	}
}

// ---- remote track, over a loopback PeerConnection pair

func loopback(t *testing.T, local webrtc.TrackLocal) *webrtc.TrackRemote {
	t.Helper()
	var se webrtc.SettingEngine
	se.SetIncludeLoopbackCandidate(true)
	se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	var me webrtc.MediaEngine
	require.NoError(t, me.RegisterDefaultCodecs())
	api := webrtc.NewAPI(webrtc.WithMediaEngine(&me), webrtc.WithSettingEngine(se))

	send, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = send.Close() })
	recv, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = recv.Close() })

	_, err = send.AddTrack(local)
	require.NoError(t, err)
	tracks := make(chan *webrtc.TrackRemote, 1)
	recv.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { tracks <- tr })

	describe := func(pc *webrtc.PeerConnection, create func(*webrtc.OfferOptions) (webrtc.SessionDescription, error)) webrtc.SessionDescription {
		desc, err := create(nil)
		require.NoError(t, err)
		gathered := webrtc.GatheringCompletePromise(pc)
		require.NoError(t, pc.SetLocalDescription(desc))
		select {
		case <-gathered:
		case <-time.After(hangGuard):
			t.Fatal("ICE gathering did not complete")
		}
		return *pc.LocalDescription()
	}
	require.NoError(t, recv.SetRemoteDescription(describe(send, send.CreateOffer)))
	answer := describe(recv, func(*webrtc.OfferOptions) (webrtc.SessionDescription, error) { return recv.CreateAnswer(nil) })
	require.NoError(t, send.SetRemoteDescription(answer))

	select {
	case tr := <-tracks:
		return tr
	case <-time.After(hangGuard):
		t.Fatalf("no remote track within %v: ICE/DTLS did not connect (sender %s, receiver %s)",
			hangGuard, send.ConnectionState(), recv.ConnectionState())
		return nil
	}
}

type pcmCollector struct {
	mu                       sync.Mutex
	pcm                      media.PCM16Sample
	closes, writesAfterClose int
	grew                     chan struct{}
}

func newCollector() *pcmCollector { return &pcmCollector{grew: make(chan struct{})} }

func (c *pcmCollector) WriteSample(s media.PCM16Sample) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closes > 0 {
		c.writesAfterClose++
	}
	c.pcm = append(c.pcm, s...)
	close(c.grew)
	c.grew = make(chan struct{})
	return nil
}

func (c *pcmCollector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closes++
	return nil
}

func (c *pcmCollector) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pcm)
}

func (c *pcmCollector) wait(t *testing.T, n int) media.PCM16Sample {
	t.Helper()
	timeout := time.After(hangGuard)
	for {
		c.mu.Lock()
		if len(c.pcm) >= n {
			defer c.mu.Unlock()
			return slices.Clone(c.pcm)
		}
		got, grew := len(c.pcm), c.grew
		c.mu.Unlock()
		select {
		case <-grew:
		case <-timeout:
			t.Fatalf("the remote track wrote only %d of %d samples", got, n)
		}
	}
}

// Publishes src from a 48 kHz local track and returns what the remote track wrote, through 200 ms past playout.
func roundTrip(t *testing.T, srcChannels int, src media.PCM16Sample, rate, channels int, opts ...PCMRemoteTrackOption) media.PCM16Sample {
	t.Helper()
	local := newLocalTrack(t, DefaultOpusSampleRate, srcChannels)
	t.Cleanup(func() { _ = local.Close() })
	col := newCollector()
	opts = append(opts, WithTargetSampleRate(rate), WithTargetChannels(channels))
	remote := newRemoteTrack(t, loopback(t, local), col, opts...)
	t.Cleanup(remote.Close)

	require.NoError(t, local.WriteSample(src))
	local.WaitForPlayout()
	return col.wait(t, col.len()+rate*channels/5)
}

func TestRemoteTrackRoundTrip(t *testing.T) {
	for _, rate := range []int{48000, 16000, 8000} {
		for _, ch := range []int{1, 2} {
			t.Run(fmt.Sprintf("%d/channels=%d", rate, ch), func(t *testing.T) {
				t.Parallel()
				hz := channelHz(ch)
				src := tones(DefaultOpusSampleRate, DefaultOpusSampleRate*3/10, toneAmp, hz...)
				requireTones(t, roundTrip(t, ch, src, rate, ch), ch, rate, hz...)
			})
		}
	}
}

// Liberate regression (media-sdk #119): two loud in-phase channels mixed to mono must clip, not wrap.
func TestRemoteTrackDownmixesLoudStereoWithoutWrapping(t *testing.T) {
	const loud = 0.98 * math.MaxInt16
	for _, rate := range []int{48000, 16000} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			t.Parallel()
			src := tones(DefaultOpusSampleRate, DefaultOpusSampleRate*3/10, loud, leftHz, leftHz)
			seg := toneBody(t, roundTrip(t, 2, src, rate, 1), 1, rate)

			peak, flips := 0, 0
			for i, v := range seg {
				peak = max(peak, int(math.Abs(float64(v))))
				if i > 0 && (v < 0) != (seg[i-1] < 0) {
					flips++
				}
			}
			require.Greater(t, peak, 4*math.MaxInt16/5, "peak")
			// Either end of the window may add one crossing; wrapping would add two per period.
			crossings := 2*leftHz*len(seg)/rate + 2
			require.LessOrEqual(t, flips, crossings, "sign flips beyond the tone's zero crossings")
		})
	}
}

func TestRemoteTrackDeliversAudioInOrder(t *testing.T) {
	const segment = DefaultOpusSampleRate * 4 / 100
	steps := []float64{300, 400, 500, 600, 700, 800, 900, 1000}
	var src media.PCM16Sample
	for _, hz := range steps {
		src = append(src, tones(DefaultOpusSampleRate, segment, toneAmp, hz)...)
	}
	for _, jitter := range []bool{true, false} {
		t.Run(fmt.Sprintf("jitter=%v", jitter), func(t *testing.T) {
			t.Parallel()
			out := roundTrip(t, 1, src, DefaultOpusSampleRate, 1, WithHandleJitter(jitter))
			start, end := toneSpan(out, 1, 0)
			require.InDelta(t, len(src), end-start, float64(packetTicks)/2, "audio went missing or repeated")
			for i, want := range steps {
				seg := out[start+i*segment+segment/4 : start+(i+1)*segment-segment/4]
				best, bestCorr := 0.0, 0.0
				for _, hz := range steps {
					if c := chanCorr(seg, 1, 0, hz, DefaultOpusSampleRate); c > bestCorr {
						best, bestCorr = hz, c
					}
				}
				require.Equal(t, want, best, "segment %d", i)
			}
		})
	}
}

package synchronizer_test

import (
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/require"

	"github.com/livekit/mediatransportutil"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer"
	"github.com/livekit/server-sdk-go/v2/pkg/synchronizer/synchronizerfakes"
)

const timeTolerance = time.Millisecond * 10

// ---------- helpers ----------

func near(t *testing.T, got, want, tol time.Duration) {
	t.Helper()
	d := got - want
	if d < 0 {
		d = -d
	}
	require.LessOrEqualf(t, d, tol, "got %v, want ~%v (±%v)", got, want, tol)
}

func pkt(ts uint32) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Timestamp: ts}}
}

func fakeAudio48k(ssrc uint32) *synchronizerfakes.FakeTrackRemote {
	f := &synchronizerfakes.FakeTrackRemote{}
	f.IDReturns("audio-1")
	f.KindReturns(webrtc.RTPCodecTypeAudio)
	f.SSRCReturns(webrtc.SSRC(ssrc))
	f.CodecReturns(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
		},
	})
	return f
}

func fakeVideo90k(ssrc uint32) *synchronizerfakes.FakeTrackRemote {
	f := &synchronizerfakes.FakeTrackRemote{}
	f.IDReturns("video-1")
	f.KindReturns(webrtc.RTPCodecTypeVideo)
	f.SSRCReturns(webrtc.SSRC(ssrc))
	f.CodecReturns(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeVP8, // codec value not important here
			ClockRate: 90000,
		},
	})
	return f
}

// ---------- tests ----------

// Initialize + same timestamp returns previous adjusted value (near zero right after init)
func TestInitialize_AndSameTimestamp(t *testing.T) {
	s := synchronizer.NewSynchronizerWithOptions(synchronizer.WithMaxTsDiff(50 * time.Millisecond))
	tr := fakeAudio48k(0xA0010001)

	ts := uint32(1_000_000)
	tsync := s.AddTrack(tr, "p1")

	tsync.Initialize(pkt(ts))

	adj0, err := tsync.GetPTS(pkt(ts))
	require.NoError(t, err)
	near(t, adj0, 0, timeTolerance)

	adj1, err := tsync.GetPTS(pkt(ts))
	require.NoError(t, err)
	require.Equal(t, adj0, adj1)
}

// 20 ms RTP step at 48 kHz → ~20 ms adjusted PTS (with small tolerance)
func TestMonotonicPTS_SmallRTPDelta(t *testing.T) {
	s := synchronizer.NewSynchronizerWithOptions(synchronizer.WithMaxTsDiff(50 * time.Millisecond))
	tr := fakeAudio48k(0xA0010002)

	ts0 := uint32(500_000)
	delta20ms := uint32(48000 * 20 / 1000) // 960 ticks

	tsync := s.AddTrack(tr, "p1")
	tsync.Initialize(pkt(ts0))

	// establish startTime
	_, _ = tsync.GetPTS(pkt(ts0))

	adj, err := tsync.GetPTS(pkt(ts0 + delta20ms))
	require.NoError(t, err)
	near(t, adj, 20*time.Millisecond, timeTolerance)
}

// Large RTP jump with tight MaxTsDiff should reset to small estimatedPTS (not ~2s)
func TestUnacceptableDrift_ResetsToEstimatedPTS(t *testing.T) {
	s := synchronizer.NewSynchronizerWithOptions(synchronizer.WithMaxTsDiff(10 * time.Millisecond))
	tr := fakeAudio48k(0xA0010003)

	ts0 := uint32(777_000)
	delta2s := uint32(48000 * 2) // 96,000 ticks

	tsync := s.AddTrack(tr, "p1")
	tsync.Initialize(pkt(ts0))

	// establish startTime
	_, _ = tsync.GetPTS(pkt(ts0))

	adj, err := tsync.GetPTS(pkt(ts0 + delta2s))
	require.NoError(t, err)
	require.LessOrEqual(t, adj, 150*time.Millisecond, "expected reset to small estimatedPTS, not ~2s")
}

func TestOnSenderReport_SlewsTowardDesiredOffset(t *testing.T) {
	const (
		maxAdjustment = 5 * time.Millisecond
		ts0           = uint32(900_000)
		stepRTP       = uint32(48000 * 20 / 1000) // 20 ms @ 48 kHz
		stepDur       = 20 * time.Millisecond
		desired       = 50 * time.Millisecond // target offset from SR
	)

	s := synchronizer.NewSynchronizerWithOptions(synchronizer.WithMaxTsDiff(1 * time.Second))
	tr := fakeAudio48k(0xA0010004)

	tsync := s.AddTrack(tr, "p1")
	tsync.Initialize(pkt(ts0))

	// Anchor wall-clock just before startTime is set.
	t0 := time.Now()
	_, _ = tsync.GetPTS(pkt(ts0)) // sets startTime ≈ t0

	cur := ts0 + stepRTP
	baseAdj, _ := tsync.GetPTS(pkt(cur)) // baseline adjusted PTS with 20ms content

	// Craft SR so offset ≈ desired:
	// offset := NTP - (startTime + pts_at_SR)
	// pts_at_SR here is 20ms, startTime ≈ t0 → set NTP to t0 + 20ms + desired
	ntp := mediatransportutil.ToNtpTime(t0.Add(stepDur + desired))
	tsync.OnSenderReport(func(d time.Duration) {})
	s.OnRTCP(&rtcp.SenderReport{
		SSRC:    uint32(tr.SSRC()),
		NTPTime: uint64(ntp),
		RTPTime: cur,
	})

	// Converge in N = ceil(desired / 5ms) steps (5ms maxAdjustment)
	N := int((desired + 5*time.Millisecond - 1) / (5 * time.Millisecond))

	for i := 0; i < N; i++ {
		cur += stepRTP
		_, err := tsync.GetPTS(pkt(cur))
		require.NoError(t, err)
	}

	// After N steps, total adjusted delta over base should be:
	// content progression (N * 20ms) + desired (slew)
	finalAdj, err := tsync.GetPTS(pkt(cur)) // same TS → returns last adjusted
	require.NoError(t, err)

	gotDelta := finalAdj - baseAdj
	wantDelta := time.Duration(N)*stepDur + desired

	near(t, gotDelta, wantDelta, timeTolerance)
}

// Regression: late video start (~2s) + tiny SR offset (~10ms) must NOT emit a big negative drift
func TestOnSenderReport_LateVideoStart_SmallSROffset_NoHugeNegativeDrift(t *testing.T) {
	const (
		lateStart = 2 * time.Second
		srOffset  = 50 * time.Millisecond
		stepSlew  = 5 * time.Millisecond // TrackSynchronizer's maxAdjustment
	)

	s := synchronizer.NewSynchronizerWithOptions(synchronizer.WithMaxTsDiff(2 * time.Second))

	// 1) Audio publishes immediately → establishes startedAt
	audio := fakeAudio48k(0xA0010005)
	tsA0 := uint32(1_000_000)
	aSync := s.AddTrack(audio, "p1")
	aSync.Initialize(pkt(tsA0))
	_, _ = aSync.GetPTS(pkt(tsA0))

	// Simulate a real late video publish
	time.Sleep(lateStart)

	// 2) Video publishes later
	video := fakeVideo90k(0x00BEEF01)
	tsV0 := uint32(2_000_000)
	vSync := s.AddTrack(video, "p1")
	vSync.Initialize(pkt(tsV0))
	_, _ = vSync.GetPTS(pkt(tsV0))

	// 3) First SR: small positive drift
	ntp := mediatransportutil.ToNtpTime(time.Now().Add(srOffset))
	var observedDrift time.Duration
	vSync.OnSenderReport(func(d time.Duration) { observedDrift = d })

	s.OnRTCP(&rtcp.SenderReport{
		SSRC:    uint32(video.SSRC()),
		NTPTime: uint64(ntp),
		RTPTime: tsV0, // equals lastTS → SR uses lastPTS at tsV0
	})

	near(t, observedDrift, srOffset, timeTolerance)

	// baseline adjusted PTS at the SR moment (same TS => returns last adjusted)
	baseAdj, err := vSync.GetPTS(pkt(tsV0))
	require.NoError(t, err)

	step33ms := uint32(90000 * 33 / 1000) // ~33 ms per 30fps frame at 90 kHz
	stepDur := 33 * time.Millisecond

	// Converge in N = ceil(srOffset / stepSlew) steps (50ms / 5ms = 10)
	N := int((srOffset + stepSlew - 1) / stepSlew)

	cur := tsV0
	var adj time.Duration

	// Drive N frames to converge
	for i := 1; i <= N; i++ {
		cur += step33ms
		adj, err = vSync.GetPTS(pkt(cur))
		require.NoError(t, err)
	}

	// After N steps, the extra beyond content cadence should be ~srOffset
	gotDelta := adj - baseAdj
	wantDelta := time.Duration(N)*stepDur + srOffset
	near(t, gotDelta, wantDelta, timeTolerance)

	// Now push more frames and ensure we DON'T keep slewing (stays near srOffset)
	const extraFrames = 8
	for i := 1; i <= extraFrames; i++ {
		cur += step33ms
		adj, err = vSync.GetPTS(pkt(cur))
		require.NoError(t, err)

		// Extra slew measured from the SR moment:
		totalContent := time.Duration(N+i) * stepDur
		extra := (adj - baseAdj) - totalContent

		// Stay within a small band around srOffset (no steady growth)
		near(t, extra, srOffset, timeTolerance)
	}
}

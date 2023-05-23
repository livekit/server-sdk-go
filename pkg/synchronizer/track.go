package synchronizer

import (
	"io"
	"math"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"

	"github.com/livekit/mediatransportutil"
	"github.com/livekit/protocol/logger"
)

const (
	ewmaWeight           = 0.9
	maxSNDropout         = 3000 // max sequence number skip
	uint32Overflow int64 = 4294967296
)

type TrackRemote interface {
	ID() string
	Codec() webrtc.RTPCodecParameters
	Kind() webrtc.RTPCodecType
	SSRC() webrtc.SSRC
}

type TrackSynchronizer struct {
	sync.Mutex
	sync   *Synchronizer
	track  TrackRemote
	logger logger.Logger

	// track info
	rtpCalc           rtpCalc
	avgSampleDuration float64

	// timing info
	startedAt int64         // starting time in unix ns
	firstTS   int64         // first RTP timestamp received
	maxPTS    time.Duration // maximum valid PTS (set after EOS)

	// previous packet info
	lastSN    uint16        // previous sequence number
	lastTS    int64         // previous RTP timestamp
	lastPTS   time.Duration // previous presentation timestamp
	lastValid bool          // previous packet did not cause a reset
	inserted  int64         // number of frames inserted

	// offsets
	snOffset  uint16        // sequence number offset (increases with each blank frame inserted
	ptsOffset time.Duration // presentation timestamp offset (used for a/v sync)

	lastPTSDrift time.Duration // track massive PTS drift, in case it's correct
}

func newTrackSynchronizer(s *Synchronizer, track TrackRemote) *TrackSynchronizer {
	t := &TrackSynchronizer{
		sync:    s,
		track:   track,
		rtpCalc: newRTPCalc(int64(track.Codec().ClockRate)),
		logger:  logger.GetLogger().WithValues("trackID", track.ID(), "kind", track.Kind().String()),
	}

	switch track.Kind() {
	case webrtc.RTPCodecTypeAudio:
		// opus default packet size is 20ms
		t.avgSampleDuration = float64(track.Codec().ClockRate) / 50
	default:
		// 30 fps for video
		t.avgSampleDuration = float64(track.Codec().ClockRate) / 30
	}

	return t
}

// Initialize should be called as soon as the first packet is received
func (t *TrackSynchronizer) Initialize(pkt *rtp.Packet) {
	now := time.Now().UnixNano()
	startedAt := t.sync.getOrSetStartedAt(now)

	t.Lock()
	t.startedAt = now
	t.firstTS = int64(pkt.Timestamp)
	t.ptsOffset = time.Duration(now - startedAt)
	t.Unlock()
}

// GetPTS will reset sequence numbers and/or offsets if necessary
// Packets are expected to be in order
func (t *TrackSynchronizer) GetPTS(pkt *rtp.Packet) (time.Duration, error) {
	t.Lock()
	defer t.Unlock()

	ts, pts, valid := t.adjust(pkt)
	t.inserted = 0

	// update frame duration if this is a new frame and both packets are valid
	if valid && t.lastValid && pkt.SequenceNumber == t.lastSN+1 {
		t.updateFrameDuration(ts)
	}

	// if past end time, return EOF
	if t.maxPTS > 0 && (pts > t.maxPTS || !valid) {
		return 0, io.EOF
	}

	// update previous values
	t.lastTS = ts
	t.lastSN = pkt.SequenceNumber
	t.lastPTS = pts
	t.lastValid = valid

	return pts, nil
}

// adjust accounts for uint32 overflow, and will reset sequence numbers or rtp time if necessary
func (t *TrackSynchronizer) adjust(pkt *rtp.Packet) (int64, time.Duration, bool) {
	// adjust sequence number and reset if needed
	pkt.SequenceNumber += t.snOffset
	if t.lastTS != 0 &&
		pkt.SequenceNumber-t.lastSN > maxSNDropout &&
		t.lastSN-pkt.SequenceNumber > maxSNDropout {

		// reset sequence numbers
		t.snOffset += t.lastSN + 1 - pkt.SequenceNumber
		pkt.SequenceNumber = t.lastSN + 1

		// reset RTP timestamps
		duration := (t.inserted + 1) * t.getFrameDurationRTP()
		ts := t.lastTS + duration
		pts := t.lastPTS + t.rtpCalc.toDuration(duration)

		t.firstTS += int64(pkt.Timestamp) - ts
		return ts, pts, false
	}

	// adjust timestamp for uint32 wrap
	ts := int64(pkt.Timestamp)
	for ts < t.lastTS {
		ts += uint32Overflow
	}

	// use the previous pts if this packet has the same timestamp
	if ts == t.lastTS {
		return ts, t.lastPTS, t.lastValid
	}

	return ts, t.getElapsed(ts) + t.ptsOffset, true
}

func (t *TrackSynchronizer) getElapsed(ts int64) time.Duration {
	return t.rtpCalc.toDuration(ts - t.firstTS)
}

// InsertFrame is used to inject frames (usually blank) into the stream
// It updates the timestamp and sequence number of the packet, as well as offsets for all future packets
func (t *TrackSynchronizer) InsertFrame(pkt *rtp.Packet) time.Duration {
	t.Lock()
	defer t.Unlock()

	pts, _ := t.insertFrameBefore(pkt, nil)
	return pts
}

// InsertFrameBefore updates the packet and offsets only if it is at least one frame duration before next
func (t *TrackSynchronizer) InsertFrameBefore(pkt *rtp.Packet, next *rtp.Packet) (time.Duration, bool) {
	t.Lock()
	defer t.Unlock()

	return t.insertFrameBefore(pkt, next)
}

func (t *TrackSynchronizer) insertFrameBefore(pkt *rtp.Packet, next *rtp.Packet) (time.Duration, bool) {
	t.inserted++
	t.snOffset++
	t.lastValid = false

	frameDurationRTP := t.getFrameDurationRTP()
	ts := t.lastTS + (t.inserted * frameDurationRTP)
	if next != nil {
		nextTS, _, _ := t.adjust(next)
		if ts+frameDurationRTP > nextTS {
			// too long, drop
			return 0, false
		}
	}

	// update packet
	pkt.SequenceNumber = t.lastSN + uint16(t.inserted)
	pkt.Timestamp = uint32(ts)

	pts := t.lastPTS + t.rtpCalc.toDuration(frameDurationRTP*t.inserted)
	return pts, true
}

func (t *TrackSynchronizer) updateFrameDuration(ts int64) {
	duration := ts - t.lastTS
	if duration > 1 {
		t.avgSampleDuration = ewmaWeight*t.avgSampleDuration + (1-ewmaWeight)*float64(duration)
	}
}

// GetFrameDuration returns frame duration in seconds
func (t *TrackSynchronizer) GetFrameDuration() time.Duration {
	t.Lock()
	defer t.Unlock()

	switch t.track.Kind() {
	case webrtc.RTPCodecTypeAudio:
		// round opus packets to 2.5ms
		round := float64(t.track.Codec().ClockRate) / 400
		return time.Duration(math.Round(t.avgSampleDuration/round)) * 2500 * time.Microsecond
	default:
		// round video to 1/3000th of a second
		round := float64(t.track.Codec().ClockRate) / 3000
		return time.Duration(math.Round(math.Round(t.avgSampleDuration/round) * 1e6 / 3))
	}
}

// getFrameDurationRTP returns frame duration in RTP time
func (t *TrackSynchronizer) getFrameDurationRTP() int64 {
	switch t.track.Kind() {
	case webrtc.RTPCodecTypeAudio:
		// round opus packets to 2.5ms
		round := float64(t.track.Codec().ClockRate) / 400
		return int64(math.Round(t.avgSampleDuration/round) * round)
	default:
		// round video to 1/30th of a second
		round := float64(t.track.Codec().ClockRate) / 3000
		return int64(math.Round(t.avgSampleDuration/round) * round)
	}
}

func (t *TrackSynchronizer) getSenderReportPTS(pkt *rtcp.SenderReport) time.Duration {
	t.Lock()
	defer t.Unlock()

	return t.getSenderReportPTSLocked(pkt)
}

func (t *TrackSynchronizer) getSenderReportPTSLocked(pkt *rtcp.SenderReport) time.Duration {
	ts := int64(pkt.RTPTime)
	for ts < t.lastTS-(uint32Overflow/2) {
		ts += uint32Overflow
	}

	return t.getElapsed(ts) + t.ptsOffset
}

// onSenderReport handles pts adjustments for a track
func (t *TrackSynchronizer) onSenderReport(pkt *rtcp.SenderReport, ntpStart time.Time) {
	t.Lock()
	defer t.Unlock()

	pts := t.getSenderReportPTSLocked(pkt)
	calculatedNTPStart := mediatransportutil.NtpTime(pkt.NTPTime).Time().Add(-pts)
	drift := calculatedNTPStart.Sub(ntpStart)
	t.logger.Debugw("Sender report", "drift", calculatedNTPStart.Sub(ntpStart))
	t.ptsOffset += drift
}

type rtpCalc struct {
	n float64
	d float64
}

func newRTPCalc(clockRate int64) rtpCalc {
	n := int64(1000000000)
	d := clockRate
	for _, i := range []int64{10, 3, 2} {
		for n%i == 0 && d%i == 0 {
			n /= i
			d /= i
		}
	}

	return rtpCalc{n: float64(n), d: float64(d)}
}

func (c rtpCalc) toDuration(x int64) time.Duration {
	return time.Duration(math.Round(float64(x) * c.n / c.d))
}

package synchronizer

import (
	"io"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"

	"github.com/livekit/mediatransportutil"
	"github.com/livekit/protocol/logger"
)

const (
	largePTSDrift         = time.Millisecond * 20
	massivePTSDrift       = time.Second
	maxPTSDrift           = int64(time.Second * 10)
	maxSNDropout          = 3000 // max sequence number skip
	uint32Overflow  int64 = 4294967296
)

type TrackSynchronizer struct {
	sync.Mutex
	sync *Synchronizer

	// track info
	trackID              string
	rtpDuration          float64 // duration in ns per unit RTP time
	frameDuration        int64   // frame duration in RTP time
	defaultFrameDuration int64   // used if no value has been recorded

	// timing info
	startedAt int64         // starting time in unix nanoseconds
	firstTS   int64         // first RTP timestamp received
	maxPTS    time.Duration // maximum valid PTS (set after EOS)

	// previous packet info
	lastSN    uint16        // previous sequence number
	lastTS    int64         // previous RTP timestamp
	lastPTS   time.Duration // previous presentation timestamp
	lastValid bool          // previous packet did not cause a reset

	// offsets
	snOffset  uint16 // sequence number offset (increases with each blank frame inserted
	ptsOffset int64  // presentation timestamp offset (used for a/v sync)

	lastPTSDrift time.Duration // track massive PTS drift, in case it's correct
}

func newTrackSynchronizer(s *Synchronizer, track *webrtc.TrackRemote) *TrackSynchronizer {
	clockRate := float64(track.Codec().ClockRate)

	t := &TrackSynchronizer{
		trackID:     track.ID(),
		sync:        s,
		rtpDuration: float64(1000000000) / clockRate,
	}

	switch track.Kind() {
	case webrtc.RTPCodecTypeAudio:
		// opus default frame size is 20ms
		t.defaultFrameDuration = int64(clockRate / 50)
	default:
		// use 24 fps for video
		t.defaultFrameDuration = int64(clockRate / 24)
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
	t.ptsOffset = now - startedAt
	t.Unlock()
}

// GetPTS will reset sequence numbers and/or offsets if necessary
// Packets are expected to be in order
func (t *TrackSynchronizer) GetPTS(pkt *rtp.Packet) (time.Duration, error) {
	t.Lock()
	defer t.Unlock()

	ts, pts, valid := t.adjust(pkt)

	// update frame duration if this is a new frame and both packets are valid
	if valid && t.lastValid && pkt.SequenceNumber == t.lastSN+1 {
		t.frameDuration = ts - t.lastTS
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
	frameDurationRTP := t.getFrameDurationRTP()

	ts := t.lastTS + frameDurationRTP

	if next != nil {
		nextTS, _, _ := t.adjust(next)
		if ts+frameDurationRTP > nextTS {
			// too long, drop
			return 0, false
		}
	}

	// update packet
	pkt.SequenceNumber = t.lastSN + 1
	pkt.Timestamp = uint32(t.lastTS + frameDurationRTP)
	t.snOffset++

	frameDuration := time.Duration(float64(frameDurationRTP) * t.rtpDuration)
	pts := t.lastPTS + frameDuration

	// update previous values
	t.lastTS = ts
	t.lastSN = pkt.SequenceNumber
	t.lastPTS = pts
	t.lastValid = false

	return pts, true
}

// adjust accounts for uint32 overflow, and will reset sequence numbers or rtp time if necessary
func (t *TrackSynchronizer) adjust(pkt *rtp.Packet) (int64, time.Duration, bool) {
	// adjust sequence number and reset if needed
	valid := t.adjustSequenceNumber(pkt)

	// adjust timestamp for uint32 wrap
	ts := int64(pkt.Timestamp)
	for ts < t.lastTS {
		ts += uint32Overflow
	}

	// use the previous pts if this packet has the same timestamp
	if ts == t.lastTS {
		return ts, t.lastPTS, t.lastValid
	}

	elapsed := t.getElapsed(ts)
	expected := time.Now().UnixNano() - t.startedAt

	// reset timestamps if needed
	if !inDelta(elapsed, expected, maxPTSDrift) {
		logger.Warnw("timestamping issue", nil,
			"expected", time.Duration(expected),
			"calculated", time.Duration(elapsed),
		)

		t.resetRTP(pkt)
		valid = false
		elapsed = t.getElapsed(ts)
	}

	return ts, time.Duration(elapsed + t.ptsOffset), valid
}

func (t *TrackSynchronizer) getElapsed(ts int64) int64 {
	return int64(float64(ts-t.firstTS) * t.rtpDuration)
}

// adjustSequenceNumber resets offsets if there is a large gap in sequence numbers
func (t *TrackSynchronizer) adjustSequenceNumber(pkt *rtp.Packet) bool {
	pkt.SequenceNumber += t.snOffset

	if t.lastTS != 0 &&
		pkt.SequenceNumber-t.lastSN > maxSNDropout &&
		t.lastSN-pkt.SequenceNumber > maxSNDropout {

		// reset
		t.snOffset = t.lastSN + 1 - pkt.SequenceNumber
		pkt.SequenceNumber = t.lastSN + 1
		t.resetRTP(pkt)
		return false
	}

	return true
}

// resetRTP assumes this packet is the start of the next frame
func (t *TrackSynchronizer) resetRTP(pkt *rtp.Packet) {
	frameDuration := t.getFrameDurationRTP()
	expectedTS := t.lastTS + frameDuration
	t.firstTS += int64(pkt.Timestamp) - expectedTS
	t.lastTS = int64(pkt.Timestamp) - frameDuration
}

// GetFrameDuration returns frame duration in seconds
func (t *TrackSynchronizer) GetFrameDuration() time.Duration {
	t.Lock()
	defer t.Unlock()

	frameDurationRTP := t.getFrameDurationRTP()
	return time.Duration(float64(frameDurationRTP) * t.rtpDuration)
}

// getFrameDurationRTP returns frame duration in RTP time
func (t *TrackSynchronizer) getFrameDurationRTP() int64 {
	if t.frameDuration != 0 {
		return t.frameDuration
	}

	return t.defaultFrameDuration
}

func (t *TrackSynchronizer) getSenderReportPTS(pkt *rtcp.SenderReport) (time.Duration, bool) {
	t.Lock()
	defer t.Unlock()

	ts := int64(pkt.RTPTime)
	for ts < t.lastTS-(uint32Overflow/2) {
		ts += uint32Overflow
	}

	elapsed := t.getElapsed(ts)
	expected := time.Now().UnixNano() - t.startedAt

	return time.Duration(elapsed + t.ptsOffset), inDelta(elapsed, expected, maxPTSDrift)
}

// onSenderReport handles pts adjustments for a track
func (t *TrackSynchronizer) onSenderReport(pkt *rtcp.SenderReport, pts time.Duration, ntpStart time.Time) {
	t.Lock()
	defer t.Unlock()

	expected := mediatransportutil.NtpTime(pkt.NTPTime).Time().Sub(ntpStart)
	if pts != expected {
		drift := expected - pts
		if absGreater(drift, largePTSDrift) {
			logger.Warnw("high pts drift", nil, "trackID", t.trackID, "pts", pts, "drift", drift)
			if absGreater(drift, massivePTSDrift) {
				if t.lastPTSDrift == 0 || absGreater(drift-t.lastPTSDrift, largePTSDrift) {
					t.lastPTSDrift = drift
					return
				}
			}
		}

		t.ptsOffset += int64(drift)
	}
}

func absGreater(a, b time.Duration) bool {
	return a > b || a < -b
}

func inDelta(a, b, delta int64) bool {
	if a > b {
		return a-b <= delta
	}
	return b-a <= delta
}

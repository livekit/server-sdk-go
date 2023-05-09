package synchronizer

import (
	"io"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"

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

	largeDrift time.Duration // track massive PTS drift, in case it's correct
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

func (t *TrackSynchronizer) Initialize(pkt *rtp.Packet) {
	now := time.Now().UnixNano()
	startedAt := t.sync.getOrSetStartedAt(now)

	t.Lock()
	t.startedAt = now
	t.firstTS = int64(pkt.Timestamp)
	t.ptsOffset = now - startedAt
	t.Unlock()
}

// GetPTS will reset sequence numbers and/or offsets if necessary, and returns presentation timestamp.
// Packets are expected to be in order.
func (t *TrackSynchronizer) GetPTS(pkt *rtp.Packet) (time.Duration, error) {
	t.Lock()
	defer t.Unlock()

	ts, pts, valid, err := t.adjust(pkt)
	if err != nil {
		return 0, err
	}

	// update frame duration if this is a new frame and both packets are valid
	if valid && t.lastValid && pkt.SequenceNumber == t.lastSN+1 {
		t.frameDuration = ts - t.lastTS
	}

	// if past end time, return EOF
	if t.maxPTS > 0 && pts > t.maxPTS {
		return 0, io.EOF
	}

	// update previous values
	t.lastTS = ts
	t.lastSN = pkt.SequenceNumber
	t.lastPTS = pts
	t.lastValid = valid

	return pts, nil
}

func (t *TrackSynchronizer) InsertFrame(pkt *rtp.Packet) time.Duration {
	t.Lock()
	defer t.Unlock()

	pts, _ := t.insertFrameBefore(pkt, nil)
	return pts
}

func (t *TrackSynchronizer) InsertFrameBefore(pkt *rtp.Packet, next *rtp.Packet) (time.Duration, bool) {
	t.Lock()
	defer t.Unlock()

	return t.insertFrameBefore(pkt, next)
}

func (t *TrackSynchronizer) insertFrameBefore(pkt *rtp.Packet, next *rtp.Packet) (time.Duration, bool) {
	frameDurationRTP := t.getFrameDurationRTP()

	ts := t.lastTS + frameDurationRTP

	if next != nil {
		nextTS, _, _, err := t.adjust(next)
		if err != nil || ts+frameDurationRTP > nextTS {
			return 0, false
		}
	}

	frameDuration := time.Duration(float64(frameDurationRTP) * t.rtpDuration)
	pts := t.lastPTS + frameDuration

	pkt.SequenceNumber = t.lastSN + 1
	pkt.Timestamp = uint32(t.lastTS + frameDurationRTP)
	t.snOffset++

	t.lastTS = ts
	t.lastSN = pkt.SequenceNumber
	t.lastPTS = pts
	t.lastValid = false

	return pts, true
}

func (t *TrackSynchronizer) adjust(pkt *rtp.Packet) (int64, time.Duration, bool, error) {
	// adjust sequence number and reset if needed
	valid := t.adjustSequenceNumber(pkt)

	// adjust timestamp for uint32 wrap
	ts := int64(pkt.Timestamp)
	for ts < t.lastTS {
		ts += uint32Overflow
	}

	// use the previous pts if this packet has the same timestamp
	if ts == t.lastTS {
		return ts, t.lastPTS, t.lastValid, nil
	}

	elapsed := t.getElapsed(ts)
	expected := time.Now().UnixNano() - t.startedAt

	// reset timestamps if needed
	if !inDelta(elapsed, expected, maxPTSDrift) {
		if t.maxPTS > 0 {
			// EOS already sent, return EOF
			return 0, 0, false, io.EOF
		}

		logger.Warnw("timestamping issue", nil,
			"expected", time.Duration(expected),
			"calculated", time.Duration(elapsed),
		)

		t.resetRTP(pkt)
		valid = false
		elapsed = t.getElapsed(ts)
	}

	return ts, time.Duration(elapsed + t.ptsOffset), valid, nil
}

func (t *TrackSynchronizer) getElapsed(ts int64) int64 {
	return int64(float64(ts-t.firstTS) * t.rtpDuration)
}

func (t *TrackSynchronizer) adjustSequenceNumber(pkt *rtp.Packet) bool {
	pkt.SequenceNumber += t.snOffset

	if t.lastTS != 0 &&
		pkt.SequenceNumber-t.lastSN > maxSNDropout &&
		t.lastSN-pkt.SequenceNumber > maxSNDropout {

		// sequence number reset
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

// getFrameDuration returns frame duration in RTP time
func (t *TrackSynchronizer) getFrameDurationRTP() int64 {
	if t.frameDuration != 0 {
		return t.frameDuration
	}

	return t.defaultFrameDuration
}

// onSenderReport handles pts adjustments for a track
// func (t *TrackSynchronizer) onSenderReport(pkt *rtcp.SenderReport, pts time.Duration, ntpStart time.Time) {
// 	expected := mediatransportutil.NtpTime(pkt.NTPTime).Time().Sub(ntpStart)
// 	if pts != expected {
// 		diff := expected - pts
// 		apply := true
// 		t.Lock()
// 		if absGreater(diff, largePTSDrift) {
// 			logger.Warnw("high pts drift", nil, "trackID", t.trackID, "pts", pts, "diff", diff)
// 			if absGreater(diff, massivePTSDrift) {
// 				// if it's the first time seeing a massive drift, ignore it
// 				if t.largeDrift == 0 || absGreater(diff-t.largeDrift, largePTSDrift) {
// 					t.largeDrift = diff
// 					apply = false
// 				}
// 			}
// 		}
// 		if apply {
// 			t.ptsOffset += int64(diff)
// 		}
// 		t.Unlock()
// 	}
// }

func inDelta(a, b, delta int64) bool {
	if a > b {
		return a-b <= delta
	}
	return b-a <= delta
}

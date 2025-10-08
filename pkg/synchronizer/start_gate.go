package synchronizer

import (
	"strings"
	"time"

	"github.com/livekit/media-sdk/jitter"
	"github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
)

// startGate is a lightweight buffer that decides when a track should be
// considered "live". Callers push packets into the gate until it returns a
// slice of packets that can be used to initialize downstream synchronisation.
// The second return value exposes how many packets were discarded while the
// gate was waiting for stability, and the third return value indicates whether
// the gate has finished its job.
type startGate interface {
	Push(pkt jitter.ExtPacket) ([]jitter.ExtPacket, int, bool)
}

// burstEstimatorGate implements startGate using the burst-estimation logic. It
// buffers packets until their arrival cadence matches the RTP timestamp spacing
// closely enough to assume we are caught up with the realtime stream. Minimum
// arrival spacing is derived from a fraction of the expected RTP interval
type burstEstimatorGate struct {
	clockRate         uint32
	maxSkewFactor     float64
	minArrivalFactor  float64
	scoreTarget       int
	maxStableDuration time.Duration
	flushAfter        time.Duration
	firstArrival      time.Time
	logger            logger.Logger

	score       int
	lastTS      uint32
	lastArrival time.Time
	hasLast     bool
	done        bool
	buffer      []jitter.ExtPacket
	maxBuffer   int

	// stats
	totalDropped int
}

func newStartGate(clockRate uint32, kind webrtc.RTPCodecType, logger logger.Logger) startGate {
	be := &burstEstimatorGate{
		clockRate:         clockRate,
		maxSkewFactor:     0.3,
		minArrivalFactor:  0.2,
		scoreTarget:       5,
		maxBuffer:         1000, // high bitrate key frames can span hundreds of packets
		maxStableDuration: time.Second,
		flushAfter:        2 * time.Second,
		logger:            logger,
	}

	if kind == webrtc.RTPCodecTypeAudio {
		be.maxBuffer = 200
	}

	return be
}

// Push feeds one packet into the gate. Once pacing stabilizes it returns the
// buffered packets that should be used to initialize the track synchronizer,
// along with the number of packets dropped while waiting and a done flag.
func (b *burstEstimatorGate) Push(pkt jitter.ExtPacket) ([]jitter.ExtPacket, int, bool) {
	if b.done {
		return nil, 0, true
	}

	elapsed := time.Duration(0)
	if !b.firstArrival.IsZero() {
		elapsed = time.Since(b.firstArrival)
	} else {
		b.firstArrival = time.Now()
	}
	if b.flushAfter > 0 && elapsed > b.flushAfter {
		ready := append(b.buffer, pkt)
		b.buffer = nil
		b.done = true
		b.logCompletion("flush_after_timeout")
		return ready, 0, true
	}

	if !b.hasLast {
		b.lastTS = pkt.Timestamp
		b.lastArrival = pkt.ReceivedAt
		b.hasLast = true
		b.buffer = append(b.buffer[:0], pkt)
		return nil, 0, false
	}

	signedTsDelta := int32(pkt.Timestamp - b.lastTS)
	arrivalDelta := pkt.ReceivedAt.Sub(b.lastArrival)

	if signedTsDelta < 0 {
		// ignore out-of-order packets while continuing to wait for stability
		return nil, 0, false
	}

	if signedTsDelta == 0 {
		// multiple packets with the same timestamp (e.g. key frame)
		b.buffer = append(b.buffer, pkt)
		if dropped := b.enforceWindow(); dropped > 0 {
			return nil, dropped, false
		}
		return nil, 0, false
	}

	b.lastTS = pkt.Timestamp
	b.lastArrival = pkt.ReceivedAt

	tsDuration := b.timestampToDuration(uint32(signedTsDelta))

	minArrival := time.Duration(float64(tsDuration) * b.minArrivalFactor)

	if arrivalDelta < minArrival {
		dropped := b.restartSequence(pkt)
		return nil, dropped, false
	}

	skew := arrivalDelta - tsDuration
	if skew < 0 {
		skew = -skew
	}

	maxSkew := time.Duration(float64(tsDuration) * b.maxSkewFactor)

	if skew > maxSkew {
		dropped := b.restartSequence(pkt)
		return nil, dropped, false
	}

	b.buffer = append(b.buffer, pkt)
	if dropped := b.enforceWindow(); dropped > 0 {
		return nil, dropped, false
	}

	b.score++

	reachedScore := b.score >= b.scoreTarget
	reachedDuration := b.totalBufferedDuration() >= b.maxStableDuration
	if reachedScore || reachedDuration {
		b.done = true
		ready := b.buffer
		b.buffer = nil
		reasons := make([]string, 0, 2)
		if reachedScore {
			reasons = append(reasons, "score_target")
		}
		if reachedDuration {
			reasons = append(reasons, "max_stable_duration")
		}
		reason := strings.Join(reasons, ",")
		b.logCompletion(reason)
		return ready, 0, true
	}

	return nil, 0, false
}

func (b *burstEstimatorGate) timestampToDuration(delta uint32) time.Duration {
	if b.clockRate == 0 {
		return 0
	}
	return time.Duration(int64(delta) * int64(time.Second) / int64(b.clockRate))
}

func (b *burstEstimatorGate) enforceWindow() int {
	if b.maxBuffer == 0 || len(b.buffer) <= b.maxBuffer {
		return 0
	}
	dropped := len(b.buffer) - b.maxBuffer
	b.buffer = b.buffer[dropped:]
	b.score = 0
	b.totalDropped += dropped
	return dropped
}

func (b *burstEstimatorGate) restartSequence(seed jitter.ExtPacket) int {
	dropped := len(b.buffer)
	b.buffer = b.buffer[:0]
	b.score = 0
	b.buffer = append(b.buffer, seed)
	b.totalDropped += dropped
	return dropped
}

func (b *burstEstimatorGate) totalBufferedDuration() time.Duration {
	if len(b.buffer) < 2 {
		return 0
	}
	first := b.buffer[0].Timestamp
	last := b.buffer[len(b.buffer)-1].Timestamp
	return b.timestampToDuration(last - first)
}

func (b *burstEstimatorGate) logCompletion(reason string) {
	if b.logger == nil {
		return
	}

	b.logger.Infow(
		"start gate sequence completed",
		"reason", reason,
		"score", b.score,
		"elapsed", time.Since(b.firstArrival),
		"droppedPackets", b.totalDropped,
	)
}

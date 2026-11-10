package media

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/opus"
	protoLogger "github.com/livekit/protocol/logger"
)

const (
	leftHz  = 440
	rightHz = 660
)

func stereoTone(rate, frames int) media.PCM16Sample {
	out := make(media.PCM16Sample, 0, 2*frames)
	for i := range frames {
		out = append(out,
			int16(0.5*math.MaxInt16*math.Sin(2*math.Pi*leftHz*float64(i)/float64(rate))),
			int16(0.5*math.MaxInt16*math.Sin(2*math.Pi*rightHz*float64(i)/float64(rate))))
	}
	return out
}

// Resampling and opus both delay the signal, so this takes the best correlation over a few ms of lag.
func toneCorr(pcm media.PCM16Sample, ch int, hz float64, rate int) float64 {
	var got []float64
	for i := ch; i < len(pcm); i += 2 {
		got = append(got, float64(pcm[i]))
	}
	best := 0.0
	for lag := range rate / 100 {
		var sxy, sxx, syy float64
		for i := lag; i < len(got); i++ {
			y := math.Sin(2 * math.Pi * hz * float64(i-lag) / float64(rate))
			sxy, sxx, syy = sxy+got[i]*y, sxx+got[i]*got[i], syy+y*y
		}
		if sxx > 0 {
			best = max(best, sxy/math.Sqrt(sxx*syy))
		}
	}
	return best
}

func requireStereoKept(t *testing.T, pcm media.PCM16Sample, rate int) {
	t.Helper()
	require.Greater(t, toneCorr(pcm, 0, leftHz, rate), 0.95, "left channel")
	require.Greater(t, toneCorr(pcm, 1, rightHz, rate), 0.95, "right channel")
}

func TestRemoteDecoderKeepsStereoAtOtherRates(t *testing.T) {
	var pkts []opus.Sample
	enc, err := opus.Encode(media.NewFrameWriter[opus.Sample](&pkts, DefaultOpusSampleRate), 2, protoLogger.GetLogger())
	require.NoError(t, err)
	for f := range slices.Chunk(stereoTone(DefaultOpusSampleRate, DefaultOpusSampleRate), 2*DefaultOpusSampleRate/50) {
		require.NoError(t, enc.WriteSample(f))
	}

	const rate = 16000
	var out media.PCM16Sample
	dec, _, err := newRemoteDecoder(media.NewPCM16BufferWriter(&out, rate), 2, protoLogger.GetLogger())
	require.NoError(t, err)
	for _, p := range pkts {
		require.NoError(t, dec.WriteSample(p))
	}
	require.NoError(t, dec.Close())
	requireStereoKept(t, out, rate)
}

func TestLocalEncoderKeepsStereoAtOtherRates(t *testing.T) {
	const rate = 16000
	var pkts []opus.Sample
	_, w, err := newLocalEncoder(media.NewFrameWriter[opus.Sample](&pkts, DefaultOpusSampleRate), rate, 2, protoLogger.GetLogger())
	require.NoError(t, err)
	for f := range slices.Chunk(stereoTone(rate, rate), 2*rate/100) {
		require.NoError(t, w.WriteSample(f))
	}

	var out media.PCM16Sample
	dec, err := opus.Decode(media.NewPCM16BufferWriter(&out, DefaultOpusSampleRate), 2, protoLogger.GetLogger())
	require.NoError(t, err)
	for _, p := range pkts {
		require.NoError(t, dec.WriteSample(p))
	}
	requireStereoKept(t, out, DefaultOpusSampleRate)
}

// 10 ms is not a whole number of samples at 22.05 or 11.025 kHz, but a second of frames must still carry a second of
// audio, and opus only takes whole 10 ms frames.
func TestLocalTrackPublishesEveryFrameRate(t *testing.T) {
	const frames = 100
	for _, rate := range []int{8000, 11025, 22050, 44100} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			var pkts []opus.Sample
			_, w, err := newLocalEncoder(media.NewFrameWriter[opus.Sample](&pkts, DefaultOpusSampleRate), rate, 1, protoLogger.GetLogger())
			require.NoError(t, err)
			written := 0
			for n := range int64(frames) {
				sz := frameSamples(n, rate, 1, defaultPCMFrameDuration)
				require.NoError(t, w.WriteSample(make(media.PCM16Sample, sz)))
				written += sz
			}
			require.Equal(t, rate, written, "a second of frames")
			require.NoError(t, w.Close())
			require.Len(t, pkts, frames, "a second of opus frames")

			var out media.PCM16Sample
			dec, err := opus.Decode(media.NewPCM16BufferWriter(&out, DefaultOpusSampleRate), 1, protoLogger.GetLogger())
			require.NoError(t, err)
			for _, p := range pkts {
				before := len(out)
				require.NoError(t, dec.WriteSample(p))
				require.Equal(t, DefaultOpusSampleRate/100, len(out)-before, "one 10 ms opus frame per packet")
			}
		})
	}
}

type closeTracker struct{ closes, writesAfterClose int }

func (c *closeTracker) WriteSample(media.PCM16Sample) error {
	if c.closes > 0 {
		c.writesAfterClose++
	}
	return nil
}
func (c *closeTracker) Close() error { c.closes++; return nil }

// The resampler drains its tail into the writer when it closes, so the writer must close once, after that.
func TestRemoteTrackClosesWriterAfterResamplerDrains(t *testing.T) {
	var pkts []opus.Sample
	enc, err := opus.Encode(media.NewFrameWriter[opus.Sample](&pkts, DefaultOpusSampleRate), 1, protoLogger.GetLogger())
	require.NoError(t, err)
	for range 5 {
		require.NoError(t, enc.WriteSample(make(media.PCM16Sample, DefaultOpusSampleRate/50)))
	}

	user := &closeTracker{}
	w := &internalPCMRemoteTrackWriter{PCMRemoteTrackWriter: user, sampleRate: 16000}
	dec, resampled, err := newRemoteDecoder(w, 1, protoLogger.GetLogger())
	require.NoError(t, err)
	track := &PCMRemoteTrack{opusWriter: dec, pcmMWriter: w, resampledPCMWriter: resampled}
	for _, p := range pkts {
		require.NoError(t, dec.WriteSample(p))
	}
	track.Close()
	require.Equal(t, 1, user.closes)
	require.Zero(t, user.writesAfterClose)
}

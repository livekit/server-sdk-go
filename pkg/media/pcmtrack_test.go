package media

import (
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

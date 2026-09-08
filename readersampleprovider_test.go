package lksdk

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/h265reader"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// h265FirstSliceInPic
// ---------------------------------------------------------------------------

func TestH265FirstSliceInPic(t *testing.T) {
	tests := []struct {
		name      string
		nalData   []byte
		wantFirst bool
		wantOK    bool
	}{
		{
			name:      "too short returns true,false",
			nalData:   []byte{0x00, 0x01},
			wantFirst: true,
			wantOK:    false,
		},
		{
			name:      "empty returns true,false",
			nalData:   nil,
			wantFirst: true,
			wantOK:    false,
		},
		{
			name:      "first_slice_segment_in_pic_flag set",
			nalData:   []byte{0x00, 0x00, 0x80}, // bit 7 of byte 2 = 1
			wantFirst: true,
			wantOK:    true,
		},
		{
			name:      "first_slice_segment_in_pic_flag clear",
			nalData:   []byte{0x00, 0x00, 0x7F}, // bit 7 of byte 2 = 0
			wantFirst: false,
			wantOK:    true,
		},
		{
			name:      "flag set with extra data",
			nalData:   []byte{0x00, 0x00, 0xC0, 0xAA, 0xBB},
			wantFirst: true,
			wantOK:    true,
		},
		{
			name:      "flag clear with extra data",
			nalData:   []byte{0x00, 0x00, 0x3F, 0xAA, 0xBB},
			wantFirst: false,
			wantOK:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFirst, gotOK := h265FirstSliceInPic(tt.nalData)
			require.Equal(t, tt.wantFirst, gotFirst)
			require.Equal(t, tt.wantOK, gotOK)
		})
	}
}

// ---------------------------------------------------------------------------
// h265AccessUnitBuilder
// ---------------------------------------------------------------------------

func TestH265AccessUnitBuilder(t *testing.T) {
	sc := []byte{0, 0, 0, 1} // Annex B start code

	t.Run("first NAL stays raw", func(t *testing.T) {
		var builder h265AccessUnitBuilder
		builder.Append([]byte{0xAA, 0xBB})
		got := builder.Bytes()
		require.Equal(t, []byte{0xAA, 0xBB}, got)
		require.Equal(t, 2, builder.Len())
	})

	t.Run("second NAL materializes annex B once", func(t *testing.T) {
		var builder h265AccessUnitBuilder
		builder.Append([]byte{0xAA, 0xBB})
		builder.Append([]byte{0xCC, 0xDD})
		got := builder.Bytes()
		want := concat(sc, []byte{0xAA, 0xBB}, sc, []byte{0xCC, 0xDD})
		require.Equal(t, want, got)
		require.Equal(t, len(want), builder.Len())
	})

	t.Run("subsequent NALs append in annex B", func(t *testing.T) {
		var builder h265AccessUnitBuilder
		builder.Append([]byte{0xAA})
		builder.Append([]byte{0xBB})
		builder.Append([]byte{0xCC, 0xDD})
		got := builder.Bytes()
		want := concat(sc, []byte{0xAA}, sc, []byte{0xBB})
		want = concat(want, sc, []byte{0xCC, 0xDD})
		require.Equal(t, want, got)
		require.Equal(t, len(want), builder.Len())
	})

	t.Run("annex b input stays annex b", func(t *testing.T) {
		var builder h265AccessUnitBuilder
		builder.AppendAnnexB([]byte{0xAA, 0xBB})
		builder.Append([]byte{0xCC})
		got := builder.Bytes()
		want := concat(sc, []byte{0xAA, 0xBB}, sc, []byte{0xCC})
		require.Equal(t, want, got)
		require.Equal(t, len(want), builder.Len())
	})
}

// ---------------------------------------------------------------------------
// H265 NextSample — access-unit assembly
// ---------------------------------------------------------------------------

func TestH265NextSample_SingleAccessUnit(t *testing.T) {
	// Build an Annex B stream: VPS + SPS + PPS + VCL(first slice) + EOF
	sc := []byte{0, 0, 0, 1}
	vps := makeH265NALData(32, []byte{0x01, 0x02})
	sps := makeH265NALData(33, []byte{0x03, 0x04})
	pps := makeH265NALData(34, []byte{0x05, 0x06})
	vcl := makeH265VCLData(1, true, []byte{0xAA, 0xBB}) // type 1, first slice

	stream := concat(sc, vps, sc, sps, sc, pps, sc, vcl)
	r := io.NopCloser(bytes.NewReader(stream))

	p := &ReaderSampleProvider{
		Mime:   webrtc.MimeTypeH265,
		reader: r,
	}
	require.NoError(t, p.OnBind())

	sample, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultH265FrameDuration, sample.Duration)
	require.NotEmpty(t, sample.Data)
	want := concat(sc, vps, sc, sps, sc, pps, sc, vcl)
	require.Equal(t, want, sample.Data)
}

func TestH265NextSample_MultipleAccessUnits(t *testing.T) {
	// Two access units: each has VCL with first_slice_in_pic set.
	sc := []byte{0, 0, 0, 1}
	vcl1 := makeH265VCLData(1, true, []byte{0x11, 0x22})
	vcl2 := makeH265VCLData(1, true, []byte{0x33, 0x44})

	stream := concat(sc, vcl1, sc, vcl2)
	r := io.NopCloser(bytes.NewReader(stream))

	p := &ReaderSampleProvider{
		Mime:   webrtc.MimeTypeH265,
		reader: r,
	}
	require.NoError(t, p.OnBind())

	// First access unit
	s1, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultH265FrameDuration, s1.Duration)
	require.Equal(t, vcl1, s1.Data)

	// Second access unit (flushed at EOF)
	s2, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultH265FrameDuration, s2.Duration)
	require.Equal(t, vcl2, s2.Data)
}

func TestH265NextSample_MultiSliceAccessUnit(t *testing.T) {
	// One access unit with two VCL NALs: first slice + continuation slice.
	sc := []byte{0, 0, 0, 1}
	vclFirst := makeH265VCLData(1, true, []byte{0x11}) // first_slice_in_pic = true
	vclCont := makeH265VCLData(1, false, []byte{0x22}) // first_slice_in_pic = false

	stream := concat(sc, vclFirst, sc, vclCont)
	r := io.NopCloser(bytes.NewReader(stream))

	p := &ReaderSampleProvider{
		Mime:   webrtc.MimeTypeH265,
		reader: r,
	}
	require.NoError(t, p.OnBind())

	sample, err := p.NextSample(context.Background())
	require.NoError(t, err)
	want := concat(sc, vclFirst, sc, vclCont)
	require.Equal(t, want, sample.Data)
}

func TestH265NextSample_NonVCLAfterVCLSplits(t *testing.T) {
	// VCL followed by VPS should split into two samples.
	sc := []byte{0, 0, 0, 1}
	vcl := makeH265VCLData(1, true, []byte{0x11})
	vps := makeH265NALData(32, []byte{0x22, 0x33})
	vcl2 := makeH265VCLData(1, true, []byte{0x44})

	stream := concat(sc, vcl, sc, vps, sc, vcl2)
	r := io.NopCloser(bytes.NewReader(stream))

	p := &ReaderSampleProvider{
		Mime:   webrtc.MimeTypeH265,
		reader: r,
	}
	require.NoError(t, p.OnBind())

	// First AU: just the VCL
	s1, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, vcl, s1.Data)

	// Second AU: VPS + VCL2
	s2, err := p.NextSample(context.Background())
	require.NoError(t, err)
	want := concat(sc, vps, sc, vcl2)
	require.Equal(t, want, s2.Data)
}

func TestH265NextSample_SuffixSEIIgnored(t *testing.T) {
	// Suffix SEI (type 40) after VCL should be ignored, not cause a split.
	sc := []byte{0, 0, 0, 1}
	vcl1 := makeH265VCLData(1, true, []byte{0x11})
	suffixSEI := makeH265NALData(40, []byte{0xFF})
	vcl2 := makeH265VCLData(1, true, []byte{0x22})

	stream := concat(sc, vcl1, sc, suffixSEI, sc, vcl2)
	r := io.NopCloser(bytes.NewReader(stream))

	p := &ReaderSampleProvider{
		Mime:   webrtc.MimeTypeH265,
		reader: r,
	}
	require.NoError(t, p.OnBind())

	// First AU: vcl1 (suffix SEI ignored, vcl2 starts new AU)
	s1, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, vcl1, s1.Data)
	// suffix SEI should not appear in the data
	require.False(t, bytes.Contains(s1.Data, suffixSEI), "s1 should not contain suffix SEI data")

	s2, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, vcl2, s2.Data)
}

func TestH265NextSample_PrefixSEIBeforeVCLSkipped(t *testing.T) {
	// A prefix SEI (type 39) with no VCL data yet should return empty sample.
	sc := []byte{0, 0, 0, 1}
	prefixSEI := makeH265NALData(39, []byte{0xFF, 0xEE})

	stream := concat(sc, prefixSEI)
	r := io.NopCloser(bytes.NewReader(stream))

	p := &ReaderSampleProvider{
		Mime:   webrtc.MimeTypeH265,
		reader: r,
	}
	require.NoError(t, p.OnBind())

	sample, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Nil(t, sample.Data)
	require.Zero(t, sample.Duration)
}

func TestH265NextSample_WithUserTimestamp(t *testing.T) {
	// Prefix SEI with packet trailer metadata, then VCL. Metadata should be attached.
	sc := []byte{0, 0, 0, 1}

	wantMeta := FrameMetadata{UserTimestamp: 9876543210, FrameId: 77}
	seiNAL := buildH265PacketTrailerSEI(wantMeta)
	vcl := makeH265VCLData(1, true, []byte{0xAA})

	stream := concat(sc, seiNAL, sc, vcl)
	r := io.NopCloser(bytes.NewReader(stream))

	p := &ReaderSampleProvider{
		Mime:                webrtc.MimeTypeH265,
		reader:              r,
		appendPacketTrailer: true,
	}
	require.NoError(t, p.OnBind())

	// First call returns the SEI-only empty sample
	s1, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Nil(t, s1.Data)

	// Second call returns the VCL frame with packet trailer
	s2, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultH265FrameDuration, s2.Duration)
	require.True(t, bytes.HasPrefix(s2.Data, vcl), "expected VCL prefix %x in sample data %x", vcl, s2.Data)

	gotMeta, ok := parsePacketTrailer(s2.Data)
	require.True(t, ok, "expected LKTS trailer in sample data")
	require.Equal(t, wantMeta.UserTimestamp, gotMeta.UserTimestamp)
	require.Equal(t, wantMeta.FrameId, gotMeta.FrameId)
}

// ---------------------------------------------------------------------------
// H265 NextSample — length-prefixed input
// ---------------------------------------------------------------------------

func TestNextNALH265LengthPrefixed(t *testing.T) {
	t.Run("parses the NAL header", func(t *testing.T) {
		vps := makeH265NALData(32, []byte{0x01, 0x02})
		r := bytes.NewReader(lengthPrefix(vps))

		nal, err := nextNALH265LengthPrefixed(r, defaultMaxNALSize)
		require.NoError(t, err)
		require.False(t, nal.ForbiddenZeroBit)
		require.Equal(t, h265reader.NalUnitType(32), nal.NalUnitType)
		require.Equal(t, uint8(0), nal.LayerID)
		require.Equal(t, uint8(1), nal.TemporalIDPlus1)
		require.Equal(t, vps, nal.Data)
	})

	t.Run("EOF at a clean boundary", func(t *testing.T) {
		_, err := nextNALH265LengthPrefixed(bytes.NewReader(nil), defaultMaxNALSize)
		require.ErrorIs(t, err, io.EOF)
	})

	t.Run("truncated payload", func(t *testing.T) {
		_, err := nextNALH265LengthPrefixed(bytes.NewReader([]byte{0, 0, 0, 8, 0x40, 0x01}), defaultMaxNALSize)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})

	t.Run("NAL shorter than the header", func(t *testing.T) {
		_, err := nextNALH265LengthPrefixed(bytes.NewReader([]byte{0, 0, 0, 1, 0x40}), defaultMaxNALSize)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})

	t.Run("zero length", func(t *testing.T) {
		_, err := nextNALH265LengthPrefixed(bytes.NewReader([]byte{0, 0, 0, 0}), defaultMaxNALSize)
		require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	})

	t.Run("exceeds max size", func(t *testing.T) {
		// prefix advertises a NAL one byte past the limit; no payload follows,
		// so a size check that ran after allocation would instead read and fail.
		_, err := nextNALH265LengthPrefixed(bytes.NewReader([]byte{0, 0, 0, 5}), 4)
		require.ErrorIs(t, err, ErrNALTooLarge)
	})
}

func TestReaderTrackWithMaxNALSize(t *testing.T) {
	// OnBind resolves the effective limit once into maxNALSize.
	bind := func(opts ...ReaderSampleProviderOption) *ReaderSampleProvider {
		p := &ReaderSampleProvider{
			Mime:                webrtc.MimeTypeH265,
			reader:              io.NopCloser(bytes.NewReader(nil)),
			h26xStreamingFormat: H26xStreamingFormatLengthPrefixed,
		}
		for _, opt := range opts {
			opt(p)
		}
		require.NoError(t, p.OnBind())
		return p
	}

	require.Equal(t, defaultMaxNALSize, bind().maxNALSize, "unset uses the default")
	require.Equal(t, defaultMaxNALSize, bind(ReaderTrackWithMaxNALSize(0)).maxNALSize, "0 keeps the default")
	require.Equal(t, 1<<20, bind(ReaderTrackWithMaxNALSize(1<<20)).maxNALSize)
}

func TestH265NextSample_LengthPrefixed_SingleAccessUnit(t *testing.T) {
	// VPS + SPS + PPS + VCL(first slice), each with a 4-byte length prefix.
	sc := []byte{0, 0, 0, 1}
	vps := makeH265NALData(32, []byte{0x01, 0x02})
	sps := makeH265NALData(33, []byte{0x03, 0x04})
	pps := makeH265NALData(34, []byte{0x05, 0x06})
	vcl := makeH265VCLData(1, true, []byte{0xAA, 0xBB})

	stream := concat(lengthPrefix(vps), lengthPrefix(sps), lengthPrefix(pps), lengthPrefix(vcl))
	p := newLengthPrefixedH265Provider(t, stream)

	sample, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultH265FrameDuration, sample.Duration)
	// the sample is emitted in Annex B form regardless of the input framing
	want := concat(sc, vps, sc, sps, sc, pps, sc, vcl)
	require.Equal(t, want, sample.Data)
}

func TestH265NextSample_LengthPrefixed_MultipleAccessUnits(t *testing.T) {
	vcl1 := makeH265VCLData(1, true, []byte{0x11, 0x22})
	vcl2 := makeH265VCLData(1, true, []byte{0x33, 0x44})

	p := newLengthPrefixedH265Provider(t, concat(lengthPrefix(vcl1), lengthPrefix(vcl2)))

	s1, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultH265FrameDuration, s1.Duration)
	require.Equal(t, vcl1, s1.Data)

	// second access unit, flushed at EOF
	s2, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultH265FrameDuration, s2.Duration)
	require.Equal(t, vcl2, s2.Data)

	_, err = p.NextSample(context.Background())
	require.ErrorIs(t, err, io.EOF)
}

func TestH265NextSample_LengthPrefixed_MultiSliceAccessUnit(t *testing.T) {
	sc := []byte{0, 0, 0, 1}
	vclFirst := makeH265VCLData(1, true, []byte{0x11})
	vclCont := makeH265VCLData(1, false, []byte{0x22})

	p := newLengthPrefixedH265Provider(t, concat(lengthPrefix(vclFirst), lengthPrefix(vclCont)))

	sample, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, concat(sc, vclFirst, sc, vclCont), sample.Data)
}

func TestH265NextSample_LengthPrefixed_NonVCLAfterVCLSplits(t *testing.T) {
	sc := []byte{0, 0, 0, 1}
	vcl := makeH265VCLData(1, true, []byte{0x11})
	vps := makeH265NALData(32, []byte{0x22, 0x33})
	vcl2 := makeH265VCLData(1, true, []byte{0x44})

	p := newLengthPrefixedH265Provider(t, concat(lengthPrefix(vcl), lengthPrefix(vps), lengthPrefix(vcl2)))

	s1, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, vcl, s1.Data)

	s2, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, concat(sc, vps, sc, vcl2), s2.Data)
}

func TestH265NextSample_LengthPrefixed_SuffixSEIIgnored(t *testing.T) {
	vcl1 := makeH265VCLData(1, true, []byte{0x11})
	suffixSEI := makeH265NALData(40, []byte{0xFF})
	vcl2 := makeH265VCLData(1, true, []byte{0x22})

	p := newLengthPrefixedH265Provider(t, concat(lengthPrefix(vcl1), lengthPrefix(suffixSEI), lengthPrefix(vcl2)))

	s1, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, vcl1, s1.Data)
	require.False(t, bytes.Contains(s1.Data, suffixSEI), "s1 should not contain suffix SEI data")

	s2, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, vcl2, s2.Data)
}

func TestH265NextSample_LengthPrefixed_PrefixSEIBeforeVCLSkipped(t *testing.T) {
	prefixSEI := makeH265NALData(39, []byte{0xFF, 0xEE})

	p := newLengthPrefixedH265Provider(t, lengthPrefix(prefixSEI))

	sample, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Nil(t, sample.Data)
	require.Zero(t, sample.Duration)
}

func TestH265NextSample_LengthPrefixed_WithUserTimestamp(t *testing.T) {
	wantMeta := FrameMetadata{UserTimestamp: 9876543210, FrameId: 77}
	seiNAL := buildH265PacketTrailerSEI(wantMeta)
	vcl := makeH265VCLData(1, true, []byte{0xAA})

	p := newLengthPrefixedH265Provider(t, concat(lengthPrefix(seiNAL), lengthPrefix(vcl)))
	p.appendPacketTrailer = true

	// first call returns the SEI-only empty sample
	s1, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Nil(t, s1.Data)

	// second call returns the VCL frame with the packet trailer
	s2, err := p.NextSample(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultH265FrameDuration, s2.Duration)
	require.True(t, bytes.HasPrefix(s2.Data, vcl), "expected VCL prefix %x in sample data %x", vcl, s2.Data)

	gotMeta, ok := parsePacketTrailer(s2.Data)
	require.True(t, ok, "expected LKTS trailer in sample data")
	require.Equal(t, wantMeta.UserTimestamp, gotMeta.UserTimestamp)
	require.Equal(t, wantMeta.FrameId, gotMeta.FrameId)
}

// Access unit aggregation only ends when the NAL that starts the next access
// unit arrives, so on a live feed every picture is held for a frame period.
// The reader here is a pipe that stops after one access unit, which is what a
// bytes.Reader cannot express: it reports EOF and flushes, hiding the wait.
func TestH265NextSample_SingleSliceFlushPublishesWithoutLookahead(t *testing.T) {
	aud := makeH265NALData(35, []byte{0x10})
	vps := makeH265NALData(32, []byte{0x01, 0x02})
	sps := makeH265NALData(33, []byte{0x03, 0x04})
	pps := makeH265NALData(34, []byte{0x05, 0x06})
	vcl := makeH265VCLData(1, true, []byte{0xAA, 0xBB})

	pipeReader, pipeWriter := io.Pipe()
	defer pipeWriter.Close()

	p := &ReaderSampleProvider{
		Mime:                 webrtc.MimeTypeH265,
		reader:               pipeReader,
		h26xStreamingFormat:  H26xStreamingFormatLengthPrefixed,
		h265SingleSliceFlush: true,
	}
	require.NoError(t, p.OnBind())

	// One access unit, and nothing after it: the next frame has not been
	// encoded yet, exactly as on a live feed.
	go func() {
		_, _ = pipeWriter.Write(concat(
			lengthPrefix(aud), lengthPrefix(vps), lengthPrefix(sps),
			lengthPrefix(pps), lengthPrefix(vcl)))
	}()

	type result struct {
		sample media.Sample
		err    error
	}
	samples := make(chan result, 4)
	go func() {
		for {
			sample, err := p.NextSample(context.Background())
			samples <- result{sample, err}
			if err != nil {
				return
			}
		}
	}()

	timeout := time.After(2 * time.Second)
	for {
		select {
		case <-timeout:
			t.Fatal("timed out: the access unit was held waiting for the next one")
		case got := <-samples:
			require.NoError(t, got.err)
			if got.sample.Duration == 0 {
				continue // the AUD, which is published on its own
			}
			require.Equal(t, defaultH265FrameDuration, got.sample.Duration)
			require.True(t, bytes.HasSuffix(got.sample.Data, vcl),
				"expected the sample to end with the slice, got %x", got.sample.Data)
			return
		}
	}
}

func TestH265OnBind_LengthPrefixedSkipsAnnexBReader(t *testing.T) {
	p := newLengthPrefixedH265Provider(t, nil)
	require.Nil(t, p.h265reader, "length-prefixed input must not build the Annex B reader")

	pAnnexB := &ReaderSampleProvider{
		Mime:                webrtc.MimeTypeH265,
		reader:              io.NopCloser(bytes.NewReader(concat([]byte{0, 0, 0, 1}, makeH265NALData(32, []byte{0x01})))),
		h26xStreamingFormat: H26xStreamingFormatAnnexB,
	}
	require.NoError(t, pAnnexB.OnBind())
	require.NotNil(t, pAnnexB.h265reader)
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// lengthPrefix frames a NAL with the 4-byte big-endian length prefix that
// H26xStreamingFormatLengthPrefixed expects.
func lengthPrefix(nal []byte) []byte {
	out := make([]byte, 4, 4+len(nal))
	binary.BigEndian.PutUint32(out, uint32(len(nal)))
	return append(out, nal...)
}

// newLengthPrefixedH265Provider builds a bound provider reading length-prefixed H265.
func newLengthPrefixedH265Provider(t *testing.T, stream []byte) *ReaderSampleProvider {
	t.Helper()

	p := &ReaderSampleProvider{
		Mime:                webrtc.MimeTypeH265,
		reader:              io.NopCloser(bytes.NewReader(stream)),
		h26xStreamingFormat: H26xStreamingFormatLengthPrefixed,
	}
	require.NoError(t, p.OnBind())
	return p
}

func concat(slices ...[]byte) []byte {
	var out []byte
	for _, s := range slices {
		out = append(out, s...)
	}
	return out
}

// makeH265NALData builds a minimal H265 NAL with the given type.
// The 2-byte NAL header encodes: F=0, Type=nalType, LayerID=0, TID=1.
func makeH265NALData(nalType h265reader.NalUnitType, payload []byte) []byte {
	// byte0: F(1) | Type(6) | LayerID_high(1) = 0 | (nalType << 1) | 0
	// byte1: LayerID_low(5) | TID(3) = 0 | 1
	b0 := byte(nalType) << 1
	b1 := byte(0x01) // TID = 1
	return append([]byte{b0, b1}, payload...)
}

// makeH265VCLData builds a VCL NAL with the first_slice_segment_in_pic_flag.
// nalType should be < 32 for VCL. The flag is in bit 7 of the third byte.
func makeH265VCLData(nalType h265reader.NalUnitType, firstSlice bool, payload []byte) []byte {
	b0 := byte(nalType) << 1
	b1 := byte(0x01)
	flagByte := byte(0x00)
	if firstSlice {
		flagByte = 0x80
	}
	data := []byte{b0, b1, flagByte}
	return append(data, payload...)
}

// buildH265PacketTrailerSEI builds a prefix SEI NAL (type 39) containing a
// user_data_unregistered message with the LKTS UUID and an LKTS packet trailer.
func buildH265PacketTrailerSEI(meta FrameMetadata) []byte {
	// 2-byte NAL header for prefix SEI (type 39)
	b0 := byte(39) << 1
	b1 := byte(0x01)
	nal := []byte{b0, b1}

	trailer := appendPacketTrailer(nil, meta)
	userData := append(packetTrailerSEIUUID[:], trailer...)

	// payloadType = 5, payloadSize = len(userData)
	nal = append(nal, 0x05, byte(len(userData)))
	nal = append(nal, userData...)
	return nal
}

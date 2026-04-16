package lksdk

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/h265reader"
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
			if gotFirst != tt.wantFirst || gotOK != tt.wantOK {
				t.Fatalf("h265FirstSliceInPic(%x) = (%v, %v), want (%v, %v)",
					tt.nalData, gotFirst, gotOK, tt.wantFirst, tt.wantOK)
			}
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
		if !bytes.Equal(got, []byte{0xAA, 0xBB}) {
			t.Fatalf("expected raw NAL, got %x", got)
		}
		if builder.Len() != 2 {
			t.Fatalf("expected len 2, got %d", builder.Len())
		}
	})

	t.Run("second NAL materializes annex B once", func(t *testing.T) {
		var builder h265AccessUnitBuilder
		builder.Append([]byte{0xAA, 0xBB})
		builder.Append([]byte{0xCC, 0xDD})
		got := builder.Bytes()
		want := concat(sc, []byte{0xAA, 0xBB}, sc, []byte{0xCC, 0xDD})
		if !bytes.Equal(got, want) {
			t.Fatalf("got %x, want %x", got, want)
		}
		if builder.Len() != len(want) {
			t.Fatalf("expected len %d, got %d", len(want), builder.Len())
		}
	})

	t.Run("subsequent NALs append in annex B", func(t *testing.T) {
		var builder h265AccessUnitBuilder
		builder.Append([]byte{0xAA})
		builder.Append([]byte{0xBB})
		builder.Append([]byte{0xCC, 0xDD})
		got := builder.Bytes()
		want := concat(sc, []byte{0xAA}, sc, []byte{0xBB})
		want = concat(want, sc, []byte{0xCC, 0xDD})
		if !bytes.Equal(got, want) {
			t.Fatalf("got %x, want %x", got, want)
		}
		if builder.Len() != len(want) {
			t.Fatalf("expected len %d, got %d", len(want), builder.Len())
		}
	})

	t.Run("annex b input stays annex b", func(t *testing.T) {
		var builder h265AccessUnitBuilder
		builder.AppendAnnexB([]byte{0xAA, 0xBB})
		builder.Append([]byte{0xCC})
		got := builder.Bytes()
		want := concat(sc, []byte{0xAA, 0xBB}, sc, []byte{0xCC})
		if !bytes.Equal(got, want) {
			t.Fatalf("got %x, want %x", got, want)
		}
		if builder.Len() != len(want) {
			t.Fatalf("expected len %d, got %d", len(want), builder.Len())
		}
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
	if err := p.OnBind(); err != nil {
		t.Fatalf("OnBind: %v", err)
	}

	sample, err := p.NextSample(context.Background())
	if err != nil {
		t.Fatalf("NextSample: %v", err)
	}
	if sample.Duration != defaultH265FrameDuration {
		t.Fatalf("expected duration %v, got %v", defaultH265FrameDuration, sample.Duration)
	}
	if len(sample.Data) == 0 {
		t.Fatal("expected non-empty sample data")
	}
	want := concat(sc, vps, sc, sps, sc, pps, sc, vcl)
	if !bytes.Equal(sample.Data, want) {
		t.Fatalf("sample data mismatch: got %x, want %x", sample.Data, want)
	}
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
	if err := p.OnBind(); err != nil {
		t.Fatalf("OnBind: %v", err)
	}

	// First access unit
	s1, err := p.NextSample(context.Background())
	if err != nil {
		t.Fatalf("NextSample 1: %v", err)
	}
	if s1.Duration != defaultH265FrameDuration {
		t.Fatalf("s1: expected duration %v, got %v", defaultH265FrameDuration, s1.Duration)
	}
	if !bytes.Equal(s1.Data, vcl1) {
		t.Fatalf("s1 data mismatch: got %x, want %x", s1.Data, vcl1)
	}

	// Second access unit (flushed at EOF)
	s2, err := p.NextSample(context.Background())
	if err != nil {
		t.Fatalf("NextSample 2: %v", err)
	}
	if s2.Duration != defaultH265FrameDuration {
		t.Fatalf("s2: expected duration %v, got %v", defaultH265FrameDuration, s2.Duration)
	}
	if !bytes.Equal(s2.Data, vcl2) {
		t.Fatalf("s2 data mismatch: got %x, want %x", s2.Data, vcl2)
	}
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
	if err := p.OnBind(); err != nil {
		t.Fatalf("OnBind: %v", err)
	}

	sample, err := p.NextSample(context.Background())
	if err != nil {
		t.Fatalf("NextSample: %v", err)
	}
	want := concat(sc, vclFirst, sc, vclCont)
	if !bytes.Equal(sample.Data, want) {
		t.Fatalf("sample data mismatch: got %x, want %x", sample.Data, want)
	}
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
	if err := p.OnBind(); err != nil {
		t.Fatalf("OnBind: %v", err)
	}

	// First AU: just the VCL
	s1, err := p.NextSample(context.Background())
	if err != nil {
		t.Fatalf("NextSample 1: %v", err)
	}
	if !bytes.Equal(s1.Data, vcl) {
		t.Fatalf("s1 data mismatch: got %x, want %x", s1.Data, vcl)
	}

	// Second AU: VPS + VCL2
	s2, err := p.NextSample(context.Background())
	if err != nil {
		t.Fatalf("NextSample 2: %v", err)
	}
	want := concat(sc, vps, sc, vcl2)
	if !bytes.Equal(s2.Data, want) {
		t.Fatalf("s2 data mismatch: got %x, want %x", s2.Data, want)
	}
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
	if err := p.OnBind(); err != nil {
		t.Fatalf("OnBind: %v", err)
	}

	// First AU: vcl1 (suffix SEI ignored, vcl2 starts new AU)
	s1, err := p.NextSample(context.Background())
	if err != nil {
		t.Fatalf("NextSample 1: %v", err)
	}
	if !bytes.Equal(s1.Data, vcl1) {
		t.Fatalf("s1 data mismatch: got %x, want %x", s1.Data, vcl1)
	}
	// suffix SEI should not appear in the data
	if bytes.Contains(s1.Data, suffixSEI) {
		t.Fatal("s1 should not contain suffix SEI data")
	}

	s2, err := p.NextSample(context.Background())
	if err != nil {
		t.Fatalf("NextSample 2: %v", err)
	}
	if !bytes.Equal(s2.Data, vcl2) {
		t.Fatalf("s2 data mismatch: got %x, want %x", s2.Data, vcl2)
	}
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
	if err := p.OnBind(); err != nil {
		t.Fatalf("OnBind: %v", err)
	}

	sample, err := p.NextSample(context.Background())
	if err != nil {
		t.Fatalf("NextSample: %v", err)
	}
	if sample.Data != nil {
		t.Fatalf("expected nil data for SEI-only sample, got %x", sample.Data)
	}
	if sample.Duration != 0 {
		t.Fatalf("expected zero duration, got %v", sample.Duration)
	}
}

func TestH265NextSample_WithUserTimestamp(t *testing.T) {
	// Prefix SEI with packet trailer metadata, then VCL. Metadata should be attached.
	sc := []byte{0, 0, 0, 1}

	wantMeta := FrameMetadata{UserTimestampUs: 9876543210, FrameId: 77}
	seiNAL := buildH265PacketTrailerSEI(wantMeta)
	vcl := makeH265VCLData(1, true, []byte{0xAA})

	stream := concat(sc, seiNAL, sc, vcl)
	r := io.NopCloser(bytes.NewReader(stream))

	p := &ReaderSampleProvider{
		Mime:                webrtc.MimeTypeH265,
		reader:              r,
		appendUserTimestamp: true,
		appendFrameId:       true,
	}
	if err := p.OnBind(); err != nil {
		t.Fatalf("OnBind: %v", err)
	}

	// First call returns the SEI-only empty sample
	s1, err := p.NextSample(context.Background())
	if err != nil {
		t.Fatalf("NextSample (SEI): %v", err)
	}
	if s1.Data != nil {
		t.Fatalf("expected nil data for SEI sample, got %x", s1.Data)
	}

	// Second call returns the VCL frame with packet trailer
	s2, err := p.NextSample(context.Background())
	if err != nil {
		t.Fatalf("NextSample (VCL): %v", err)
	}
	if s2.Duration != defaultH265FrameDuration {
		t.Fatalf("expected duration %v, got %v", defaultH265FrameDuration, s2.Duration)
	}
	if !bytes.HasPrefix(s2.Data, vcl) {
		t.Fatalf("expected VCL prefix %x in sample data %x", vcl, s2.Data)
	}

	gotMeta, ok := parsePacketTrailer(s2.Data)
	if !ok {
		t.Fatal("expected LKTS trailer in sample data")
	}
	if gotMeta.UserTimestampUs != wantMeta.UserTimestampUs {
		t.Fatalf("timestamp mismatch: got %d, want %d", gotMeta.UserTimestampUs, wantMeta.UserTimestampUs)
	}
	if gotMeta.FrameId != wantMeta.FrameId {
		t.Fatalf("frame_id mismatch: got %d, want %d", gotMeta.FrameId, wantMeta.FrameId)
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

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

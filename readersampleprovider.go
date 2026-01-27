// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package lksdk

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
	"github.com/pion/webrtc/v4/pkg/media/h264reader"
	"github.com/pion/webrtc/v4/pkg/media/h265reader"
	"github.com/pion/webrtc/v4/pkg/media/ivfreader"

	"github.com/livekit/server-sdk-go/v2/pkg/oggreader"
)

const (
	// defaults to 30 fps
	defaultH264FrameDuration = 33 * time.Millisecond
	defaultH265FrameDuration = 33 * time.Millisecond
	// G.711 uses 20ms frames at 8kHz = 160 samples
	defaultG711FrameDuration   = 20 * time.Millisecond
	defaultG711SamplesPerFrame = 160
)

// ---------------------------------

type H26xStreamingFormat int

const (
	H26xStreamingFormatAnnexB H26xStreamingFormat = iota
	H26xStreamingFormatLengthPrefixed
)

func (f H26xStreamingFormat) String() string {
	switch f {
	case H26xStreamingFormatAnnexB:
		return "AnnexB"
	case H26xStreamingFormatLengthPrefixed:
		return "LengthPrefixed"
	default:
		return fmt.Sprintf("Unknown: %d", f)
	}
}

// ---------------------------------

// ReaderSampleProvider provides samples by reading from an io.ReadCloser implementation
type ReaderSampleProvider struct {
	// Configuration
	Mime                string
	FrameDuration       time.Duration
	OnWriteComplete     func()
	AudioLevel          uint8
	trackOpts           []LocalTrackOptions
	h26xStreamingFormat H26xStreamingFormat
	appendUserTimestamp bool

	// When appendUserTimestamp is enabled, we will attempt to parse timestamps from
	// H264 SEI user_data_unregistered NALs that precede frame NALs.
	// We then stash the parsed timestamp and attach it to the next frame as an LKTS trailer.
	pendingUserTimestampUs  int64
	hasPendingUserTimestamp bool

	// Allow various types of ingress
	reader io.ReadCloser

	// for vp8/vp9
	ivfReader     *ivfreader.IVFReader
	ivfTimebase   float64
	lastTimestamp uint64

	// for h264
	h264reader *h264reader.H264Reader

	// for h265
	h265reader *h265reader.H265Reader

	// for ogg
	oggReader *oggreader.OggReader

	// for wav (PCMU/PCMA)
	wavReader *wavReader
}

type ReaderSampleProviderOption func(*ReaderSampleProvider)

func ReaderTrackWithMime(mime string) func(provider *ReaderSampleProvider) {
	return func(provider *ReaderSampleProvider) {
		provider.Mime = mime
	}
}

func ReaderTrackWithFrameDuration(duration time.Duration) func(provider *ReaderSampleProvider) {
	return func(provider *ReaderSampleProvider) {
		provider.FrameDuration = duration
	}
}

func ReaderTrackWithOnWriteComplete(f func()) func(provider *ReaderSampleProvider) {
	return func(provider *ReaderSampleProvider) {
		provider.OnWriteComplete = f
	}
}

func ReaderTrackWithRTCPHandler(f func(rtcp.Packet)) func(provider *ReaderSampleProvider) {
	return func(provider *ReaderSampleProvider) {
		provider.trackOpts = append(provider.trackOpts, WithRTCPHandler(f))
	}
}

func ReaderTrackWithSampleOptions(opts ...LocalTrackOptions) func(provider *ReaderSampleProvider) {
	return func(provider *ReaderSampleProvider) {
		provider.trackOpts = append(provider.trackOpts, opts...)
	}
}

func ReaderTrackWithH26xStreamingFormat(h26xStreamingFormat H26xStreamingFormat) func(provider *ReaderSampleProvider) {
	return func(provider *ReaderSampleProvider) {
		provider.h26xStreamingFormat = h26xStreamingFormat
	}
}

func readerTrackWithWavReader(wr *wavReader) func(provider *ReaderSampleProvider) {
	return func(provider *ReaderSampleProvider) {
		provider.wavReader = wr
	}
}

// ReaderTrackWithUserTimestamp enables attaching the custom LKTS trailer
// (timestamp_us + magic) to outgoing encoded frame payloads.
// This currently supports H264.
func ReaderTrackWithUserTimestamp(enabled bool) func(provider *ReaderSampleProvider) {
	return func(provider *ReaderSampleProvider) {
		provider.appendUserTimestamp = enabled
	}
}

// NewLocalFileTrack creates an *os.File reader for NewLocalReaderTrack
func NewLocalFileTrack(file string, options ...ReaderSampleProviderOption) (*LocalTrack, error) {
	// File health check
	var err error
	if _, err = os.Stat(file); err != nil {
		return nil, err
	}

	// Open the file
	fp, err := os.Open(file)
	if err != nil {
		return nil, err
	}

	// Determine mime type from extension
	var mime string
	switch filepath.Ext(file) {
	case ".h264":
		mime = webrtc.MimeTypeH264
	case ".ivf":
		buf := make([]byte, 4)
		_, err = fp.ReadAt(buf, 8)
		if err != nil {
			return nil, err
		}
		switch {
		case string(buf[:3]) == "VP8":
			mime = webrtc.MimeTypeVP8
		case string(buf[:3]) == "VP9":
			mime = webrtc.MimeTypeVP9
		case string(buf) == "AV01":
			mime = webrtc.MimeTypeAV1
		default:
			_ = fp.Close()
			return nil, ErrCannotDetermineMime
		}
		_, _ = fp.Seek(0, 0)
	case ".h265":
		mime = webrtc.MimeTypeH265
	case ".ogg":
		mime = webrtc.MimeTypeOpus
	case ".wav":
		// Parse WAV header to determine format (PCMU or PCMA)
		var wavReader *wavReader
		wavReader, mime, err = detectWavFormat(fp)
		if err != nil {
			_ = fp.Close()
			return nil, ErrCannotDetermineMime
		}
		options = append(options, readerTrackWithWavReader(wavReader))
	default:
		_ = fp.Close()
		return nil, ErrCannotDetermineMime
	}

	track, err := NewLocalReaderTrack(fp, mime, options...)
	if err != nil {
		_ = fp.Close()
		return nil, err
	}
	return track, nil
}

// NewLocalReaderTrack uses io.ReadCloser interface to adapt to various ingress types
// - mime: has to be one of webrtc.MimeType... (e.g. webrtc.MimeTypeOpus)
func NewLocalReaderTrack(in io.ReadCloser, mime string, options ...ReaderSampleProviderOption) (*LocalTrack, error) {
	provider := &ReaderSampleProvider{
		h26xStreamingFormat: H26xStreamingFormatAnnexB,
		Mime:                mime,
		reader:              in,
		// default audio level to be fairly loud
		AudioLevel: 15,
	}
	for _, opt := range options {
		opt(provider)
	}

	var clockRate uint32

	// check if mime type is supported
	switch provider.Mime {
	case webrtc.MimeTypeH264, webrtc.MimeTypeH265, webrtc.MimeTypeVP8, webrtc.MimeTypeVP9, webrtc.MimeTypeAV1:
		clockRate = 90000
	case webrtc.MimeTypeOpus:
		clockRate = 48000
	case webrtc.MimeTypePCMU, webrtc.MimeTypePCMA:
		clockRate = 8000
	default:
		return nil, ErrUnsupportedFileType
	}

	// Create sample track & bind handler
	track, err := NewLocalTrack(webrtc.RTPCodecCapability{MimeType: provider.Mime, ClockRate: clockRate}, provider.trackOpts...)
	if err != nil {
		return nil, err
	}
	track.OnBind(func() {
		if err := track.StartWrite(provider, provider.OnWriteComplete); err != nil {
			track.log.Errorw("Could not start writing", err)
		}
	})

	return track, nil
}

func (p *ReaderSampleProvider) OnBind() error {
	// If we are not closing on unbind, don't do anything on rebind
	if p.ivfReader != nil || p.h264reader != nil || p.oggReader != nil || p.h265reader != nil || p.wavReader != nil {
		return nil
	}

	var err error
	switch p.Mime {
	case webrtc.MimeTypeH264:
		if p.h26xStreamingFormat == H26xStreamingFormatAnnexB {
			p.h264reader, err = h264reader.NewReaderWithOptions(p.reader, h264reader.WithIncludeSEI(true))
		}
	case webrtc.MimeTypeH265:
		p.h265reader, err = h265reader.NewReader(p.reader)
	case webrtc.MimeTypeVP8, webrtc.MimeTypeVP9, webrtc.MimeTypeAV1:
		var ivfHeader *ivfreader.IVFFileHeader
		p.ivfReader, ivfHeader, err = ivfreader.NewWith(p.reader)
		if err == nil {
			p.ivfTimebase = float64(ivfHeader.TimebaseNumerator) / float64(ivfHeader.TimebaseDenominator)
		}
	case webrtc.MimeTypeOpus:
		p.oggReader, _, err = oggreader.NewOggReader(p.reader)
	case webrtc.MimeTypePCMU, webrtc.MimeTypePCMA:
		if p.wavReader == nil {
			p.wavReader, _, err = newWavReader(p.reader)
		}
	default:
		err = ErrUnsupportedFileType
	}
	if err != nil {
		_ = p.reader.Close()
		return err
	}
	return nil
}

func (p *ReaderSampleProvider) OnUnbind() error {
	return nil
}

func (p *ReaderSampleProvider) Close() error {
	if p.reader != nil {
		return p.reader.Close()
	}
	return nil
}

func (p *ReaderSampleProvider) CurrentAudioLevel() uint8 {
	return p.AudioLevel
}

func (p *ReaderSampleProvider) NextSample(ctx context.Context) (media.Sample, error) {
	sample := media.Sample{}
	switch p.Mime {
	case webrtc.MimeTypeH264:
		var (
			nalUnitType h264reader.NalUnitType
			nalUnitData []byte
			err         error
		)
		switch p.h26xStreamingFormat {
		case H26xStreamingFormatLengthPrefixed:
			nalUnitType, nalUnitData, err = nextNALH264LengthPrefixed(p.reader)
			if err != nil {
				return sample, err
			}

		default:
			var nal *h264reader.NAL
			nal, err = p.h264reader.NextNAL()
			if err != nil {
				return sample, err
			}
			nalUnitType = nal.UnitType
			nalUnitData = nal.Data
		}

		if nalUnitType == h264reader.NalUnitTypeSEI {
			if p.appendUserTimestamp {
				if ts, ok := parseH264SEIUserTimestamp(nalUnitData); ok {
					p.pendingUserTimestampUs = ts
					p.hasPendingUserTimestamp = true
				}
			} else {
				// If SEI, clear the data and do not return a frame (try next NAL)
				sample.Data = nil
				sample.Duration = 0
				return sample, nil
			}
		}

		isFrame := false
		switch nalUnitType {
		case h264reader.NalUnitTypeCodedSliceDataPartitionA,
			h264reader.NalUnitTypeCodedSliceDataPartitionB,
			h264reader.NalUnitTypeCodedSliceDataPartitionC,
			h264reader.NalUnitTypeCodedSliceIdr,
			h264reader.NalUnitTypeCodedSliceNonIdr:
			isFrame = true
		}

		sample.Data = nalUnitData
		if !isFrame {
			// return it without duration
			return sample, nil
		}

		// Attach the LKTS trailer to the encoded frame payload when enabled.
		// If we didn't see a preceding timestamp, we still append a trailer with
		// a zero timestamp.
		if p.appendUserTimestamp {
			ts := int64(0)
			if p.hasPendingUserTimestamp {
				ts = p.pendingUserTimestampUs
				p.hasPendingUserTimestamp = false
				p.pendingUserTimestampUs = 0
			}
			sample.Data = appendUserTimestampTrailer(sample.Data, ts)
		}

		sample.Duration = defaultH264FrameDuration

	case webrtc.MimeTypeH265:
		var (
			isFrame    bool
			needPrefix bool
		)

		for {
			nal, err := p.h265reader.NextNAL()
			if err != nil {
				return sample, err
			}

			// aggregate vps,sps,pps into a single AP packet (chrome requires this)
			if nal.NalUnitType == 32 || nal.NalUnitType == 33 || nal.NalUnitType == 34 {
				sample.Data = append(sample.Data, []byte{0, 0, 0, 1}...) // add NAL prefix
				sample.Data = append(sample.Data, nal.Data...)
				needPrefix = true
				continue
			}

			if needPrefix {
				sample.Data = append(sample.Data, []byte{0, 0, 0, 1}...) // add NAL prefix
				sample.Data = append(sample.Data, nal.Data...)
			} else {
				sample.Data = nal.Data
			}

			if !isFrame {
				isFrame = nal.NalUnitType < 32
			}

			if !isFrame {
				// return it without duration
				return sample, nil
			}

			sample.Duration = defaultH265FrameDuration
			break
		}

	case webrtc.MimeTypeVP8, webrtc.MimeTypeVP9, webrtc.MimeTypeAV1:
		frame, header, err := p.ivfReader.ParseNextFrame()
		if err != nil {
			return sample, err
		}
		delta := header.Timestamp - p.lastTimestamp
		sample.Data = frame
		sample.Duration = time.Duration(p.ivfTimebase*float64(delta)*1000) * time.Millisecond
		p.lastTimestamp = header.Timestamp
	case webrtc.MimeTypeOpus:
		payload, err := p.oggReader.ReadPacket()
		if err != nil {
			return sample, err
		}

		sample.Data = payload
		sample.Duration, err = oggreader.ParsePacketDuration(payload)
		if err != nil {
			return sample, err
		}
	case webrtc.MimeTypePCMU, webrtc.MimeTypePCMA:
		frame, err := p.wavReader.readFrame()
		if err != nil {
			return sample, err
		}
		sample.Data = frame
		// Calculate duration based on actual frame size
		// For G.711: 160 samples = 20ms, so each sample = 0.125ms
		// Duration = (frame length in bytes) * (20ms / 160 samples)
		if len(frame) == defaultG711SamplesPerFrame {
			sample.Duration = defaultG711FrameDuration
		} else {
			// Partial frame: calculate duration proportionally
			sample.Duration = time.Duration(len(frame)) * defaultG711FrameDuration / defaultG711SamplesPerFrame
		}
	}

	if p.FrameDuration > 0 {
		sample.Duration = p.FrameDuration
	}
	return sample, nil
}

// --------------------------------------------------

// wavReader reads WAV files containing PCMU (mulaw) or PCMA (alaw) audio
type wavReader struct {
	reader          io.Reader
	dataSize        int64
	samplesPerFrame int
	bytesRead       int64 // Track bytes read from data chunk
}

// WAV file format constants
const (
	wavFormatPCMU = 0x0007 // G.711 μ-law
	wavFormatPCMA = 0x0006 // G.711 A-law
)

// newWavReader parses a WAV file header and returns a reader for PCMU/PCMA samples
func newWavReader(r io.Reader) (*wavReader, string, error) {
	var riffHeader [12]byte
	if _, err := io.ReadFull(r, riffHeader[:]); err != nil {
		return nil, "", fmt.Errorf("failed to read RIFF header: %w", err)
	}

	if string(riffHeader[0:4]) != "RIFF" {
		return nil, "", fmt.Errorf("not a RIFF file")
	}

	if string(riffHeader[8:12]) != "WAVE" {
		return nil, "", fmt.Errorf("not a WAVE file")
	}

	var formatCode uint16
	var channels uint16
	var sampleRate uint32
	var bitsPerSample uint16
	foundFmt := false

	for {
		var chunkHeader [8]byte
		if _, err := io.ReadFull(r, chunkHeader[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return nil, "", fmt.Errorf("failed to read chunk header: %w", err)
		}

		chunkID := string(chunkHeader[0:4])
		chunkSize := int64(binary.LittleEndian.Uint32(chunkHeader[4:8]))

		switch chunkID {
		case "fmt ":
			if chunkSize < 16 {
				return nil, "", fmt.Errorf("fmt chunk too small: %d bytes (minimum 16)", chunkSize)
			}

			var fmtBase [16]byte
			if _, err := io.ReadFull(r, fmtBase[:]); err != nil {
				return nil, "", fmt.Errorf("failed to read fmt chunk: %w", err)
			}

			formatCode = binary.LittleEndian.Uint16(fmtBase[0:2])
			channels = binary.LittleEndian.Uint16(fmtBase[2:4])
			sampleRate = binary.LittleEndian.Uint32(fmtBase[4:8])
			bitsPerSample = binary.LittleEndian.Uint16(fmtBase[14:16])

			if formatCode != wavFormatPCMU && formatCode != wavFormatPCMA {
				return nil, "", fmt.Errorf("unsupported WAV format code: 0x%04x (expected PCMU 0x0007 or PCMA 0x0006)", formatCode)
			}
			if channels != 1 {
				return nil, "", fmt.Errorf("only mono (1 channel) WAV files are supported, got %d channels", channels)
			}
			if sampleRate != 8000 {
				return nil, "", fmt.Errorf("expected 8000 Hz sample rate for G.711, got %d", sampleRate)
			}
			if bitsPerSample != 8 {
				return nil, "", fmt.Errorf("expected 8 bits per sample for G.711, got %d", bitsPerSample)
			}

			if remaining := chunkSize - 16; remaining > 0 {
				if _, err := io.CopyN(io.Discard, r, remaining); err != nil {
					return nil, "", fmt.Errorf("failed to read fmt chunk extension: %w", err)
				}
			}
			foundFmt = true

		case "data":
			if !foundFmt {
				return nil, "", fmt.Errorf("fmt chunk not found before data")
			}

			var mime string
			switch formatCode {
			case wavFormatPCMU:
				mime = webrtc.MimeTypePCMU
			case wavFormatPCMA:
				mime = webrtc.MimeTypePCMA
			default:
				return nil, "", fmt.Errorf("unsupported WAV format code: 0x%04x (expected PCMU 0x0007 or PCMA 0x0006)", formatCode)
			}

			return &wavReader{
				reader:          r,
				dataSize:        chunkSize,
				samplesPerFrame: defaultG711SamplesPerFrame,
				bytesRead:       0,
			}, mime, nil

		default:
			if _, err := io.CopyN(io.Discard, r, chunkSize); err != nil {
				return nil, "", fmt.Errorf("failed to skip %s chunk: %w", chunkID, err)
			}
		}

		// WAV format requires chunks to be word-aligned (even size)
		// If chunk size is odd, there's a padding byte that must be skipped
		if chunkSize%2 != 0 {
			var pad [1]byte
			if _, err := io.ReadFull(r, pad[:]); err != nil {
				return nil, "", fmt.Errorf("failed to read padding byte: %w", err)
			}
		}
	}

	if !foundFmt {
		return nil, "", fmt.Errorf("fmt chunk not found")
	}

	return nil, "", fmt.Errorf("data chunk not found")
}

// readFrame reads one frame of G.711 audio (160 samples = 20ms at 8kHz)
// It respects dataSize and stops reading at the end of the audio data chunk
func (w *wavReader) readFrame() ([]byte, error) {
	// Check if we've read all available data
	remaining := w.dataSize - w.bytesRead
	if remaining <= 0 {
		return nil, io.EOF
	}

	// Calculate how many bytes to read (up to one frame, but not more than remaining)
	frameSize := w.samplesPerFrame
	bytesToRead := frameSize
	if remaining < int64(bytesToRead) {
		bytesToRead = int(remaining)
	}

	buf := make([]byte, bytesToRead)
	n, err := io.ReadFull(w.reader, buf)
	if err == io.EOF && n == 0 {
		return nil, io.EOF
	}
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}

	// Update bytes read counter
	w.bytesRead += int64(n)

	// Return the frame (may be partial if at end of data)
	return buf[:n], nil
}

// minimal length-prefixed NAL reeader
func nextNALH264LengthPrefixed(r io.Reader) (h264reader.NalUnitType, []byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}

	n := int(binary.BigEndian.Uint32(hdr[:]))
	if n <= 0 {
		return 0, nil, io.ErrUnexpectedEOF
	}

	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, err
	}

	return h264reader.NalUnitType(buf[0] & 0x1F), buf, nil
}

func detectWavFormat(r io.Reader) (*wavReader, string, error) {
	// Parse WAV header to get mime type and create wavReader
	// Stream ends at start of data chunk - no seeking needed
	wavReader, mime, err := newWavReader(r)
	if err != nil {
		return nil, "", err
	}

	return wavReader, mime, nil
}

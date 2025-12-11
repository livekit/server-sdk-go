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
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	userTimestamp       bool

	// When userTimestamp is enabled, SEI user_data_unregistered timestamps
	// are assumed to precede the actual coded frame NAL. We stash the parsed
	// timestamp and attach it to the next frame as an LKTS trailer.
	pendingUserTimestampUs  int64
	hasPendingUserTimestamp bool

	// Allow various types of ingress
	reader io.ReadCloser

	// for vp8/vp9
	ivfReader     *ivfreader.IVFReader
	ivfTimebase   float64
	lastTimestamp uint64

	// for h264
	h264AnnexBReader *h264AnnexBReader

	// for h265
	h265reader *h265reader.H265Reader

	// for ogg
	oggReader *oggreader.OggReader
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

// ReaderTrackWithUserTimestamp enables attaching the custom LKTS trailer
// (timestamp_us + magic) to outgoing encoded frame payloads. For H264, the
// timestamp is parsed from SEI user_data_unregistered NAL units (when present)
// and assumed to apply to the next coded frame.
func ReaderTrackWithUserTimestamp(enabled bool) func(provider *ReaderSampleProvider) {
	return func(provider *ReaderSampleProvider) {
		provider.userTimestamp = enabled
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
		buf := make([]byte, 3)
		_, err = fp.ReadAt(buf, 8)
		if err != nil {
			return nil, err
		}
		switch string(buf) {
		case "VP8":
			mime = webrtc.MimeTypeVP8
		case "VP9":
			mime = webrtc.MimeTypeVP9
		default:
			_ = fp.Close()
			return nil, ErrCannotDetermineMime
		}
		_, _ = fp.Seek(0, 0)
	case ".h265":
		mime = webrtc.MimeTypeH265
	case ".ogg":
		mime = webrtc.MimeTypeOpus
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
	case webrtc.MimeTypeH264, webrtc.MimeTypeH265, webrtc.MimeTypeVP8, webrtc.MimeTypeVP9:
		clockRate = 90000
	case webrtc.MimeTypeOpus:
		clockRate = 48000
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
	if p.ivfReader != nil || p.h264AnnexBReader != nil || p.oggReader != nil || p.h265reader != nil {
		return nil
	}

	var err error
	switch p.Mime {
	case webrtc.MimeTypeH264:
		if p.h26xStreamingFormat == H26xStreamingFormatAnnexB {
			// NOTE: pion/webrtc's h264reader drops SEI NAL units, but we need to
			// observe them (e.g. for userTimestamp SEI user_data_unregistered).
			// So we use our own minimal Annex-B NAL splitter that does not skip SEI.
			p.h264AnnexBReader = newH264AnnexBReader(p.reader)
		}
	case webrtc.MimeTypeH265:
		p.h265reader, err = h265reader.NewReader(p.reader)
	case webrtc.MimeTypeVP8, webrtc.MimeTypeVP9:
		var ivfHeader *ivfreader.IVFFileHeader
		p.ivfReader, ivfHeader, err = ivfreader.NewWith(p.reader)
		if err == nil {
			p.ivfTimebase = float64(ivfHeader.TimebaseNumerator) / float64(ivfHeader.TimebaseDenominator)
		}
	case webrtc.MimeTypeOpus:
		p.oggReader, _, err = oggreader.NewOggReader(p.reader)
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

		// Read NALs until we get a non-SEI NAL. Historically (via pion/webrtc's
		// Annex-B reader) we skipped SEI entirely; we preserve that behavior while
		// still consuming SEI for timestamp extraction.
		for {
			switch p.h26xStreamingFormat {
			case H26xStreamingFormatLengthPrefixed:
				nalUnitType, nalUnitData, err = nextNALH264LengthPrefixed(p.reader)
			default:
				nalUnitType, nalUnitData, err = p.h264AnnexBReader.NextNAL()
			}
			if err != nil {
				return sample, err
			}

			// Parse SEI user_data_unregistered messages (UUID + timestamp_us) when present.
			if nalUnitType == h264reader.NalUnitTypeSEI {
				if p.userTimestamp {
					if ts, ok := parseH264SEIUserDataUnregistered(nalUnitData); ok {
						p.pendingUserTimestampUs = ts
						p.hasPendingUserTimestamp = true
					}
				}
				// Do not output SEI as a sample; apply it to the next coded frame.
				continue
			}

			break
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
		// a zero timestamp (matches the behavior in rust-sdks/webrtc-sys).
		if p.userTimestamp {
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

	case webrtc.MimeTypeVP8, webrtc.MimeTypeVP9:
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
	}

	if p.FrameDuration > 0 {
		sample.Duration = p.FrameDuration
	}
	return sample, nil
}

// parseH264SEIUserDataUnregistered parses SEI NAL units (type 6) carrying
// user_data_unregistered messages and prints the decoded UUID and timestamp.
//
// Expected payload format (after the NAL header byte):
//
//	payloadType  = 5 (user_data_unregistered)
//	payloadSize  = 24
//	UUID         = 16 bytes
//	timestamp_us = 8 bytes, big-endian
//	trailing     = 0x80 (stop bits + padding)
func parseH264SEIUserDataUnregistered(nalData []byte) (int64, bool) {
	if len(nalData) < 2 {
		logger.Infow("H264 SEI user_data_unregistered: nal too short", "nal_len", len(nalData))
		return 0, false
	}

	// Skip NAL header (first byte).
	payload := nalData[1:]
	i := 0

	// Parse payloadType (can be extended with 0xFF bytes).
	payloadType := 0
	for i < len(payload) && payload[i] == 0xFF {
		payloadType += 255
		i++
	}
	if i >= len(payload) {
		logger.Infow("H264 SEI user_data_unregistered: payloadType truncated", "payload_len", len(payload))
		return 0, false
	}
	payloadType += int(payload[i])
	i++

	logger.Debugw("H264 SEI NAL parsed payloadType", "payloadType", payloadType)

	// We only care about user_data_unregistered (type 5).
	if payloadType != 5 {
		logger.Infow("H264 SEI NAL not user_data_unregistered, skipping", "payloadType", payloadType)
		return 0, false
	}

	// Parse payloadSize (can be extended with 0xFF bytes).
	payloadSize := 0
	for i < len(payload) && payload[i] == 0xFF {
		payloadSize += 255
		i++
	}
	if i >= len(payload) {
		logger.Infow("H264 SEI user_data_unregistered: payloadSize truncated", "payload_len", len(payload))
		return 0, false
	}
	payloadSize += int(payload[i])
	i++

	if payloadSize < 24 || len(payload) < i+payloadSize {
		// Not enough data for UUID (16) + timestamp (8).
		logger.Infow(
			"H264 SEI user_data_unregistered: insufficient data for UUID + timestamp",
			"payloadSize", payloadSize,
			"payload_len", len(payload),
			"offset", i,
		)
		return 0, false
	}

	userData := payload[i : i+payloadSize]
	uuidBytes := userData[:16]
	tsBytes := userData[16:24]

	timestampUS := binary.BigEndian.Uint64(tsBytes)

	// Format UUID as 8-4-4-4-12 hex segments.
	uuid := fmt.Sprintf("%x-%x-%x-%x-%x",
		uuidBytes[0:4],
		uuidBytes[4:6],
		uuidBytes[6:8],
		uuidBytes[8:10],
		uuidBytes[10:16],
	)

	logger.Infow(
		"H264 SEI user_data_unregistered parsed",
		"uuid", uuid,
		"timestamp_us", timestampUS,
		"payloadSize", payloadSize,
	)

	return int64(timestampUS), true
}

// --------------------------------------------------

// h264AnnexBReader is a minimal H.264 Annex-B NAL unit splitter.
//
// Unlike pion/webrtc's h264reader, it does NOT drop SEI NAL units.
// It returns NAL data without the Annex-B start code (00 00 01 / 00 00 00 01),
// but including the NAL header byte.
type h264AnnexBReader struct {
	r      *bufio.Reader
	synced bool
}

func newH264AnnexBReader(in io.Reader) *h264AnnexBReader {
	return &h264AnnexBReader{
		r: bufio.NewReaderSize(in, 4096),
	}
}

func (r *h264AnnexBReader) syncToStartCode() error {
	zeros := 0
	for {
		b, err := r.r.ReadByte()
		if err != nil {
			return err
		}
		if b == 0 {
			zeros++
			continue
		}
		if b == 1 && zeros >= 2 {
			// Found 00 00 01 (or 00 00 00 01). Start code fully consumed.
			return nil
		}
		zeros = 0
	}
}

func (r *h264AnnexBReader) NextNAL() (h264reader.NalUnitType, []byte, error) {
	if !r.synced {
		if err := r.syncToStartCode(); err != nil {
			return 0, nil, err
		}
		r.synced = true
	}

	nal := make([]byte, 0, 1024)
	zeros := 0

	for {
		b, err := r.r.ReadByte()
		if err != nil {
			if err == io.EOF && len(nal) > 0 {
				break
			}
			return 0, nil, err
		}

		nal = append(nal, b)

		if b == 0 {
			zeros++
			continue
		}

		if b == 1 && zeros >= 2 {
			// We just appended a start code for the *next* NAL.
			// Trim it from the current NAL and return.
			prefixZeros := 2
			if zeros > 2 {
				prefixZeros = 3
			}
			trim := prefixZeros + 1 // +1 for the trailing 0x01

			if len(nal) >= trim {
				nal = nal[:len(nal)-trim]
			} else {
				nal = nil
			}

			// If we encountered consecutive start codes, keep scanning.
			if len(nal) == 0 {
				nal = make([]byte, 0, 1024)
				zeros = 0
				continue
			}
			break
		}

		zeros = 0
	}

	if len(nal) == 0 {
		return 0, nil, io.EOF
	}

	return h264reader.NalUnitType(nal[0] & 0x1F), nal, nil
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

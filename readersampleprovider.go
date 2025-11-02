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
	if p.ivfReader != nil || p.h264reader != nil || p.oggReader != nil || p.h265reader != nil {
		return nil
	}

	var err error
	switch p.Mime {
	case webrtc.MimeTypeH264:
		if p.h26xStreamingFormat == H26xStreamingFormatAnnexB {
			p.h264reader, err = h264reader.NewReader(p.reader)
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

// --------------------------------------------------

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

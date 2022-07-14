package lksdk

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/h264reader"
	"github.com/pion/webrtc/v3/pkg/media/ivfreader"
	"github.com/pion/webrtc/v3/pkg/media/oggreader"
)

const (
	// defaults to 30 fps
	defaultH264FrameDuration = 33 * time.Millisecond
	defaultOpusFrameDuration = 20 * time.Millisecond
)

// ReaderSampleProvider provides samples by reading from an io.ReadCloser implementation
type ReaderSampleProvider struct {
	// Configuration
	Mime            string
	FrameDuration   time.Duration
	OnWriteComplete func()
	AudioLevel      uint8
	trackOpts       []LocalSampleTrackOptions

	// Allow various types of ingress
	reader io.ReadCloser

	// for vp8
	ivfreader     *ivfreader.IVFReader
	ivfTimebase   float64
	lastTimestamp uint64

	// for h264
	h264reader *h264reader.H264Reader

	// for ogg
	oggreader   *oggreader.OggReader
	lastGranule uint64
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

// NewLocalFileTrack creates an *os.File reader for NewLocalReaderTrack
func NewLocalFileTrack(file string, options ...ReaderSampleProviderOption) (*LocalSampleTrack, error) {
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
		mime = webrtc.MimeTypeVP8
	case ".ogg":
		mime = webrtc.MimeTypeOpus
	default:
		return nil, ErrCannotDetermineMime
	}

	return NewLocalReaderTrack(fp, mime, options...)
}

// NewLocalReaderTrack uses io.ReadCloser interface to adapt to various ingress types
// - mime: has to be one of webrtc.MimeType... (e.g. webrtc.MimeTypeOpus)
func NewLocalReaderTrack(in io.ReadCloser, mime string, options ...ReaderSampleProviderOption) (*LocalSampleTrack, error) {
	provider := &ReaderSampleProvider{
		Mime:   mime,
		reader: in,
		// default audio level to be fairly loud
		AudioLevel: 15,
	}
	for _, opt := range options {
		opt(provider)
	}

	// check if mime type is supported
	switch provider.Mime {
	case webrtc.MimeTypeH264, webrtc.MimeTypeOpus, webrtc.MimeTypeVP8:
	// allow
	default:
		return nil, ErrUnsupportedFileType
	}

	// Create sample track & bind handler
	track, err := NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: provider.Mime}, provider.trackOpts...)
	if err != nil {
		return nil, err
	}

	track.OnBind(func() {
		if err := track.StartWrite(provider, provider.OnWriteComplete); err != nil {
			logger.Error(err, "Could not start writing")
		}
	})
	return track, nil
}

func (p *ReaderSampleProvider) OnBind() error {
	var err error
	switch p.Mime {
	case webrtc.MimeTypeH264:
		p.h264reader, err = h264reader.NewReader(p.reader)
	case webrtc.MimeTypeVP8:
		var ivfheader *ivfreader.IVFFileHeader
		p.ivfreader, ivfheader, err = ivfreader.NewWith(p.reader)
		if err == nil {
			p.ivfTimebase = float64(ivfheader.TimebaseNumerator) / float64(ivfheader.TimebaseDenominator)
		}
	case webrtc.MimeTypeOpus:
		p.oggreader, _, err = oggreader.NewWith(p.reader)
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
	return p.reader.Close()
}

func (p *ReaderSampleProvider) CurrentAudioLevel() uint8 {
	return p.AudioLevel
}

func (p *ReaderSampleProvider) NextSample() (media.Sample, error) {
	sample := media.Sample{}
	switch p.Mime {
	case webrtc.MimeTypeH264:
		nal, err := p.h264reader.NextNAL()
		if err != nil {
			return sample, err
		}

		isFrame := false
		switch nal.UnitType {
		case h264reader.NalUnitTypeCodedSliceDataPartitionA,
			h264reader.NalUnitTypeCodedSliceDataPartitionB,
			h264reader.NalUnitTypeCodedSliceDataPartitionC,
			h264reader.NalUnitTypeCodedSliceIdr,
			h264reader.NalUnitTypeCodedSliceNonIdr:
			isFrame = true
		}

		sample.Data = nal.Data
		if !isFrame {
			// return it without duration
			return sample, nil
		}
		sample.Duration = defaultH264FrameDuration
	case webrtc.MimeTypeVP8:
		frame, header, err := p.ivfreader.ParseNextFrame()
		if err != nil {
			return sample, err
		}
		delta := header.Timestamp - p.lastTimestamp
		sample.Data = frame
		sample.Duration = time.Duration(p.ivfTimebase*float64(delta)*1000) * time.Millisecond
		p.lastTimestamp = header.Timestamp
	case webrtc.MimeTypeOpus:
		pageData, pageHeader, err := p.oggreader.ParseNextPage()
		if err != nil {
			return sample, err
		}
		sampleCount := float64(pageHeader.GranulePosition - p.lastGranule)
		p.lastGranule = pageHeader.GranulePosition

		sample.Data = pageData
		sample.Duration = time.Duration((sampleCount/48000)*1000) * time.Millisecond
		if sample.Duration == 0 {
			sample.Duration = defaultOpusFrameDuration
		}
	}

	if p.FrameDuration > 0 {
		sample.Duration = p.FrameDuration
	}
	return sample, nil
}

package lksdk

import (
	"os"
	"path/filepath"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/h264reader"
	"github.com/pion/webrtc/v3/pkg/media/ivfreader"
	"github.com/pion/webrtc/v3/pkg/media/oggreader"
)

const (
	defaultH264FrameDuration = 33 * time.Millisecond
	defaultOpusFrameDuration = 20 * time.Millisecond
)

// FileSampleProvider allows LiveKit to publish a file into a room
type FileSampleProvider struct {
	Mime            string
	FileName        string
	FrameDuration   time.Duration
	OnWriteComplete func()
	file            *os.File

	// for vp8
	ivfreader *ivfreader.IVFReader

	// for h264
	h264reader *h264reader.H264Reader

	// for ogg
	oggreader   *oggreader.OggReader
	lastGranule uint64
}

type FileSampleProviderOption func(*FileSampleProvider)

func FileTrackWithMime(mime string) func(provider *FileSampleProvider) {
	return func(provider *FileSampleProvider) {
		provider.Mime = mime
	}
}

func FileTrackWithFrameDuration(duration time.Duration) func(provider *FileSampleProvider) {
	return func(provider *FileSampleProvider) {
		provider.FrameDuration = duration
	}
}

func FileTrackWithOnWriteComplete(f func()) func(provider *FileSampleProvider) {
	return func(provider *FileSampleProvider) {
		provider.OnWriteComplete = f
	}
}

func NewLocalFileTrack(file string, options ...FileSampleProviderOption) (*LocalSampleTrack, error) {
	provider := &FileSampleProvider{
		FileName: file,
	}
	for _, opt := range options {
		opt(provider)
	}

	// detect mime if not set
	if provider.Mime == "" {
		ext := filepath.Ext(file)
		switch ext {
		case ".h264":
			provider.Mime = webrtc.MimeTypeH264
		case ".ogg":
			provider.Mime = webrtc.MimeTypeOpus
		case ".ivf":
			provider.Mime = webrtc.MimeTypeVP8
		default:
			return nil, ErrCannotDetermineMime
		}
	}

	switch provider.Mime {
	case webrtc.MimeTypeH264, webrtc.MimeTypeOpus, webrtc.MimeTypeVP8:
	// allow
	default:
		return nil, ErrUnsupportedFileType
	}

	if _, err := os.Stat(file); err != nil {
		return nil, err
	}

	track, err := NewLocalSampleTrack(webrtc.RTPCodecCapability{MimeType: provider.Mime})
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

func (p *FileSampleProvider) OnBind() error {
	var err error
	p.file, err = os.Open(p.FileName)
	if err != nil {
		return err
	}
	switch p.Mime {
	case webrtc.MimeTypeH264:
		p.h264reader, err = h264reader.NewReader(p.file)
	case webrtc.MimeTypeVP8:
		var ivfheader *ivfreader.IVFFileHeader
		p.ivfreader, ivfheader, err = ivfreader.NewWith(p.file)
		if err == nil {
			p.FrameDuration = time.Millisecond * time.Duration((float32(ivfheader.TimebaseNumerator)/float32(ivfheader.TimebaseDenominator))*1000)
		}
	case webrtc.MimeTypeOpus:
		p.oggreader, _, err = oggreader.NewWith(p.file)
	default:
		err = ErrUnsupportedFileType
	}
	if err != nil {
		_ = p.file.Close()
		return err
	}
	return nil
}

func (p *FileSampleProvider) OnUnbind() error {
	return p.file.Close()
}

func (p *FileSampleProvider) NextSample() (media.Sample, error) {
	sample := media.Sample{}
	switch p.Mime {
	case webrtc.MimeTypeH264:
		nal, err := p.h264reader.NextNAL()
		if err != nil {
			return sample, err
		}

		sample.Data = nal.Data
		sample.Duration = defaultH264FrameDuration
	case webrtc.MimeTypeVP8:
		frame, _, err := p.ivfreader.ParseNextFrame()
		if err != nil {
			return sample, err
		}
		sample.Data = frame
	case webrtc.MimeTypeOpus:
		pageData, pageHeader, err := p.oggreader.ParseNextPage()
		if err != nil {
			return sample, err
		}
		sampleCount := float64(pageHeader.GranulePosition - p.lastGranule)
		p.lastGranule = pageHeader.GranulePosition

		sample.Data = pageData
		sample.Duration = time.Duration((sampleCount/48000)*1000) * time.Millisecond
	}

	if p.FrameDuration > 0 {
		sample.Duration = p.FrameDuration
	}
	return sample, nil
}

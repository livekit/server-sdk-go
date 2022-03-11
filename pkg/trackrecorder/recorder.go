package trackrecorder

import (
	"context"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/pkg/samplebuilder"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
)

type Recorder interface {
	Start(context.Context, *webrtc.TrackRemote)
	Stop()
	Sink() Sink
}

type recorder struct {
	ctx    context.Context
	cancel context.CancelFunc

	sink Sink
	mw   media.Writer
	sb   *samplebuilder.SampleBuilder
}

func New(filename string, codec webrtc.RTPCodecParameters, opts ...samplebuilder.Option) (Recorder, error) {
	sink, err := NewFileSink(filename)
	if err != nil {
		return nil, err
	}
	return NewWith(sink, codec)
}

func NewWith(sink Sink, codec webrtc.RTPCodecParameters, opts ...samplebuilder.Option) (Recorder, error) {
	mw, err := createMediaWriter(sink, codec)
	if err != nil {
		return nil, err
	}
	return &recorder{
		sink: sink,
		mw:   mw,
		sb:   createSampleBuilder(codec, opts...),
	}, nil
}

func (r *recorder) Start(ctx context.Context, track *webrtc.TrackRemote) {
	// Copy context since it's a good practice
	r.ctx, r.cancel = context.WithCancel(ctx)

	// Start recording in a goroutine
	go r.startRecording(track)
}

func (r *recorder) Stop() {
	// Signal goroutine to stop
	r.cancel()
}

func (r *recorder) Sink() Sink {
	return r.sink
}

func (r *recorder) startRecording(track *webrtc.TrackRemote) {
	var err error
	defer func() {
		// Log any errors
		if err != nil {
			logger.Errorw("recorder error: ", err)
		}

		// Close sink
		err = r.sink.Close()
		if err != nil {
			logger.Errorw("sink error: ", err)
		}
	}()

	// Process RTP packets forever until stopped
	var packet *rtp.Packet
	for {
		select {
		case <-r.ctx.Done():
			return
		default:
			// Read RTP stream
			packet, _, err = track.ReadRTP()
			if err != nil {
				return
			}

			// Write packet to sink
			err = r.writeToSink(packet)
			if err != nil {
				return
			}
		}
	}
}

func (r *recorder) writeToSink(p *rtp.Packet) (err error) {
	// If no sample buffer is used, write directly to sink
	if r.sb == nil {
		return r.mw.WriteRTP(p)
	}

	// If sample buffer is used, write to buffer first
	r.sb.Push(p)

	// And from the buffered packets, write to sink
	for _, p := range r.sb.PopPackets() {
		err = r.mw.WriteRTP(p)
		if err != nil {
			return err
		}
	}

	return nil
}

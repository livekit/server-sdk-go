package lksdk

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gammazero/deque"
	"github.com/google/uuid"
	media "github.com/livekit/media-sdk"
	opus "github.com/livekit/media-sdk/opus"
	rtp "github.com/livekit/media-sdk/rtp"
	protoLogger "github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
	"go.uber.org/atomic"
)

const (
	DefaultOpusSampleRate     = 48000
	DefaultOpusSampleDuration = 20 * time.Millisecond

	// using the smallest opus frame duration to minimize
	// the silent filler chunks
	defaultPCMSampleDuration = 10000 * time.Microsecond
)

type pcmChunk struct {
	sample        media.PCM16Sample
	frameDuration time.Duration
}

type EncodingLocalAudioTrack struct {
	*webrtc.TrackLocalStaticSample

	opusWriter         media.WriteCloser[opus.Sample]
	pcmWriter          media.WriteCloser[media.PCM16Sample]
	resampledPCMWriter media.WriteCloser[media.PCM16Sample]

	sourceSampleRate int
	frameDuration    time.Duration
	sourceChannels   int
	chunksPerSample  int

	// int16 to support a LE/BE PCM16 chunk that has a high byte and low byte
	chunkBuffer *deque.Deque[int16]

	mu     sync.Mutex
	cond   *sync.Cond
	closed atomic.Bool
}

// TODO: test stereo with resampler
func NewEncodingLocalAudioTrack(sourceSampleRate int, sourceChannels int, logger protoLogger.Logger) (*EncodingLocalAudioTrack, error) {
	if sourceChannels <= 0 || sourceChannels > 2 || sourceSampleRate <= 0 {
		return nil, errors.New("invalid source sample rate or channels")
	}

	id := uuid.New().String()[:5]
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "go_track"+id, "go_stream"+id)
	if err != nil {
		return nil, err
	}

	// opusWriter writes opus samples to the track
	opusWriter := media.FromSampleWriter[opus.Sample](track, DefaultOpusSampleRate, defaultPCMSampleDuration)
	// pcmWriter encodes opus samples from PCM16 samples and writes them to opusWriter
	pcmWriter, err := opus.Encode(opusWriter, sourceChannels, logger)
	if err != nil {
		return nil, err
	}

	// resampled writer resamples the PCM16 samples from sourceSampleRate to DefaultOpusSampleRate
	// and writes them to pcmWriter. If no resampling is needed, we directly point resampledPCMWriter to pcmWriter.
	resampledPCMWriter := pcmWriter
	if sourceSampleRate != DefaultOpusSampleRate {
		resampledPCMWriter = media.ResampleWriter(pcmWriter, sourceSampleRate)
	}

	t := &EncodingLocalAudioTrack{
		TrackLocalStaticSample: track,
		opusWriter:             opusWriter,
		pcmWriter:              pcmWriter,
		resampledPCMWriter:     resampledPCMWriter,
		sourceSampleRate:       sourceSampleRate,
		frameDuration:          defaultPCMSampleDuration,
		sourceChannels:         sourceChannels,
		chunkBuffer:            new(deque.Deque[int16]),
		chunksPerSample:        (sourceSampleRate * sourceChannels * int(defaultPCMSampleDuration/time.Nanosecond)) / 1e9,
	}

	t.cond = sync.NewCond(&t.mu)

	go t.processSamples()
	return t, nil
}

func (t *EncodingLocalAudioTrack) pushChunksToBuffer(sample media.PCM16Sample) {
	for _, chunk := range sample {
		t.chunkBuffer.PushBack(chunk)
	}
}

func (t *EncodingLocalAudioTrack) waitUntilBufferHasChunks(count int) bool {
	var didWait bool

	for t.chunkBuffer.Len() < count && !t.closed.Load() {
		t.cond.Wait()
		didWait = true
	}

	return didWait
}

func (t *EncodingLocalAudioTrack) getChunksFromBuffer() (media.PCM16Sample, bool) {
	chunks := make(media.PCM16Sample, t.chunksPerSample)

	// if buffer is empty at the start, we wait until it has chunks
	didWait := t.waitUntilBufferHasChunks(t.chunksPerSample)

	if t.closed.Load() {
		return nil, false
	}

	for i := 0; i < t.chunksPerSample; i++ {
		chunks[i] = t.chunkBuffer.PopFront()
	}

	return chunks, didWait
}

func (t *EncodingLocalAudioTrack) WriteSample(sample media.PCM16Sample) error {
	if t.closed.Load() {
		return errors.New("track is closed")
	}

	t.mu.Lock()
	t.pushChunksToBuffer(sample)
	t.cond.Broadcast()
	t.mu.Unlock()
	return nil
}

func (t *EncodingLocalAudioTrack) processSamples() {
	ticker := time.NewTicker(t.frameDuration)
	defer ticker.Stop()

	for {
		if t.closed.Load() {
			break
		}

		t.mu.Lock()
		sample, didWait := t.getChunksFromBuffer()
		if sample != nil {
			t.resampledPCMWriter.WriteSample(sample)
		}
		if didWait {
			ticker.Reset(t.frameDuration)
		}
		t.mu.Unlock()
		<-ticker.C
	}
}

func (t *EncodingLocalAudioTrack) Close() {
	firstClose := t.closed.CompareAndSwap(false, true)
	// avoid closing the writer multiple times
	if firstClose {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.cond.Broadcast()
		t.chunkBuffer.Clear()
		t.resampledPCMWriter.Close()
		t.pcmWriter.Close()
		t.opusWriter.Close()
	}
}

type DecodingRemoteTrackParams struct {
	HandleJitter bool
}

type DecodingRemoteTrackOption func(*DecodingRemoteTrackParams)

func WithHandleJitter(handleJitter bool) DecodingRemoteTrackOption {
	return func(p *DecodingRemoteTrackParams) {
		p.HandleJitter = handleJitter
	}
}

type DecodingRemoteAudioTrack struct {
	*webrtc.TrackRemote
	channels   int
	sampleRate int
	once       sync.Once

	opusWriter         media.WriteCloser[opus.Sample]
	pcmMWriter         media.WriteCloser[media.PCM16Sample]
	resampledPCMWriter media.WriteCloser[media.PCM16Sample]
	logger             protoLogger.Logger
}

// TODO: fix channel messiness, webm writer in the example needs number of channels at the time of init
// and NewDecodedAudioTrack is called afterwards. But, we also need to check for channels in the init function
// to make sure user does not pass stereo as target channels for a mono track. Any suggestions on how to handle this?
// TODO: test stereo with resampler
func NewDecodingRemoteAudioTrack(track *webrtc.TrackRemote, writer *media.WriteCloser[media.PCM16Sample], targetSampleRate int, targetChannels int, opts ...DecodingRemoteTrackOption) (*DecodingRemoteAudioTrack, error) {
	if track.Codec().MimeType != webrtc.MimeTypeOpus {
		return nil, errors.New("track is not opus")
	}

	if targetChannels <= 0 || targetChannels > 2 || targetSampleRate <= 0 {
		return nil, errors.New("invalid target channels or sample rate")
	}

	options := &DecodingRemoteTrackParams{
		HandleJitter: true,
	}
	for _, opt := range opts {
		opt(options)
	}

	// resampledPCMWriter resamples the PCM16 samples from DefaultOpusSampleRate to targetSampleRate and
	// writes them to the writer. If no resampling is needed, we directly point resampledPCMWriter to writer.
	resampledPCMWriter := *writer
	if targetSampleRate != DefaultOpusSampleRate {
		resampledPCMWriter = media.ResampleWriter(*writer, targetSampleRate)
	}

	// opus writer takes opus samples, decodes them to PCM16 samples
	// and writes them to the pcmMWriter
	opusWriter, err := opus.Decode(resampledPCMWriter, targetChannels, protoLogger.GetLogger())
	if err != nil {
		return nil, err
	}

	t := &DecodingRemoteAudioTrack{
		TrackRemote:        track,
		opusWriter:         opusWriter,
		pcmMWriter:         *writer,
		resampledPCMWriter: resampledPCMWriter,
		sampleRate:         targetSampleRate,
		channels:           targetChannels,
		logger:             protoLogger.GetLogger(),
	}

	go t.process(options.HandleJitter)
	return t, nil
}

func (t *DecodingRemoteAudioTrack) process(handleJitter bool) {
	// Handler takes RTP packets and writes the payload to opusWriter
	var h rtp.Handler = rtp.NewMediaStreamIn[opus.Sample](t.opusWriter)
	if handleJitter {
		h = rtp.HandleJitter(int(t.TrackRemote.Codec().ClockRate), h)
	}

	// HandleLoop takes RTP packets from the track and writes them to the handler
	// TODO: handle concealment
	err := rtp.HandleLoop(t.TrackRemote, h)
	if err != nil && !errors.Is(err, io.EOF) {
		t.logger.Errorw("error handling rtp from track", err)
	}
}

func (t *DecodingRemoteAudioTrack) Channels() int {
	return t.channels
}

func (t *DecodingRemoteAudioTrack) SampleRate() int {
	return t.sampleRate
}

func (t *DecodingRemoteAudioTrack) Close() {
	if t.pcmMWriter.String() != t.resampledPCMWriter.String() {
		t.pcmMWriter.Close()
	}
	// opus writer closes resampledPCMWriter internally
	t.opusWriter.Close()

}

// ------------------------------------------------------------------

func isOpusPacketStereo(payload []byte) bool {
	// the table-of-contents (TOC) header byte is the first byte of the payload
	// it is composed of a configuration number, "config", a stereo flag, "s", and a frame count code, "c"
	// https://datatracker.ietf.org/doc/html/rfc6716#section-3.1
	tocByte := payload[0]
	// TOC byte format:
	//   0
	// 	 0 1 2 3 4 5 6 7
	//  +-+-+-+-+-+-+-+-+
	//  | config  |s| c |
	//  +-+-+-+-+-+-+-+-+
	// the 's' bit is stereo bit
	return tocByte&0x04 != 0
}

func DetermineOpusChannels(track *webrtc.TrackRemote) int {
	rtpPacket, _, err := track.ReadRTP()
	if err != nil {
		protoLogger.GetLogger().Errorw("error reading rtp from track", err)
		return 1
	}

	stereo := isOpusPacketStereo(rtpPacket.Payload)
	if stereo {
		return 2
	}
	return 1
}

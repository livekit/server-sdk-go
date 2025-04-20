package lksdk

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gammazero/deque"
	audio "github.com/livekit/mediatransportutil/pkg/audio"
	opus "github.com/livekit/mediatransportutil/pkg/audio/opus"
	rtp "github.com/livekit/mediatransportutil/pkg/audio/rtp"
	protoLogger "github.com/livekit/protocol/logger"
	"github.com/pion/webrtc/v4"
	"go.uber.org/atomic"
)

const (
	DefaultOpusSampleRate     = 48000
	DefaultOpusSampleDuration = 20 * time.Millisecond

	// DefaultPCMSampleDuration = 20 * time.Millisecond
	DefaultPCMSampleDuration = 5000 * time.Microsecond

	// smallestOpusFrameDuration = 2500 * time.Microsecond
	smallestOpusFrameDuration = 5000 * time.Microsecond
)

type pcmChunk struct {
	sample        audio.PCM16Sample
	frameDuration time.Duration
}

type EncodedAudioTrack struct {
	*webrtc.TrackLocalStaticSample

	opusWriter         audio.WriteCloser[opus.Sample]
	pcmWriter          audio.WriteCloser[audio.PCM16Sample]
	resampledPCMWriter audio.WriteCloser[audio.PCM16Sample]

	sourceSampleRate int
	frameDuration    time.Duration
	sourceChannels   int

	chunkBuffer *deque.Deque[pcmChunk]
	started     sync.Once
	ticker      *time.Ticker

	closed atomic.Bool
	mu     sync.Mutex
}

// TODO: test stereo with resampler
func NewEncodedAudioTrack(sourceSampleRate int, sourceChannels int, logger protoLogger.Logger) (*EncodedAudioTrack, error) {
	track, err := webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "test", "test")
	if err != nil {
		return nil, err
	}

	// opusWriter writes opus samples to the track
	opusWriter := audio.FromSampleWriter[opus.Sample](track, DefaultOpusSampleRate, DefaultPCMSampleDuration)
	// pcmWriter encodes opus samples from PCM16 samples and writes them to opusWriter
	pcmWriter, err := opus.Encode(opusWriter, sourceChannels, logger)
	if err != nil {
		return nil, err
	}

	resampledPCMWriter := pcmWriter
	if sourceSampleRate != DefaultOpusSampleRate {
		resampledPCMWriter = audio.ResampleWriter(pcmWriter, sourceSampleRate)
	}

	t := &EncodedAudioTrack{
		TrackLocalStaticSample: track,
		opusWriter:             opusWriter,
		pcmWriter:              pcmWriter,
		resampledPCMWriter:     resampledPCMWriter,
		sourceSampleRate:       sourceSampleRate,
		frameDuration:          DefaultPCMSampleDuration,
		sourceChannels:         sourceChannels,
		chunkBuffer:            new(deque.Deque[pcmChunk]),
	}

	go t.processSamples()
	return t, nil
}

func (t *EncodedAudioTrack) pushChunksToBuffer(chunks []pcmChunk) {
	for _, chunk := range chunks {
		t.chunkBuffer.PushBack(chunk)
	}
}

func (t *EncodedAudioTrack) WriteSample(sample audio.PCM16Sample) error {
	if t.closed.Load() {
		return errors.New("track is closed")
	}

	t.mu.Lock()
	chunks := splitPCM16SampleToTargetDuration(sample, t.sourceSampleRate, t.sourceChannels, DefaultPCMSampleDuration)
	t.pushChunksToBuffer(chunks)
	// t.cond.Broadcast()
	t.mu.Unlock()
	return nil
}

func (t *EncodedAudioTrack) processSamples() {
	silentChunk := generateSilentPCM16Chunk(t.sourceSampleRate, t.sourceChannels, smallestOpusFrameDuration)
	resetTicker := false

	// wait for the first sample before starting the ticker
	var chunk pcmChunk
	var lastChunkDuration time.Duration

	// write the first chunk and start the ticker
	// or a silent chunk if buffer is empty
	if t.chunkBuffer.Len() > 0 {
		chunk = t.chunkBuffer.PopFront()
	} else {
		chunk = silentChunk
	}
	lastChunkDuration = chunk.frameDuration

	t.resampledPCMWriter.WriteSample(chunk.sample)
	// ticker to avoid writing a sample for the duration of the last written chunk
	t.ticker = time.NewTicker(chunk.frameDuration)

	for range t.ticker.C {
		if t.closed.Load() {
			return
		}

		t.mu.Lock()
		// write silent chunk if buffer is empty
		if t.chunkBuffer.Len() != 0 {
			chunk = t.chunkBuffer.PopFront()
		} else {
			// 2.5 ms is the smallest chunk duration supported by Opus.
			// It should be small enough to not affect audio when a chunk gets written
			// to the buffer while silent chunk is being processed at the other end.
			chunk = silentChunk
		}

		// In case some chunks are smaller than the default chunk duration,
		// or in general the last chunk that was written. e.g. silent chunk duration is 2.5ms
		// which is less than the default chunk duration of 5ms.
		// This ensures that the ticker duration is updated with the current chunk duration.
		if chunk.frameDuration != lastChunkDuration {
			resetTicker = true
		}
		lastChunkDuration = chunk.frameDuration

		t.resampledPCMWriter.WriteSample(chunk.sample)

		if resetTicker {
			t.ticker.Reset(lastChunkDuration)
			resetTicker = false
		}
		t.mu.Unlock()
	}

	t.chunkBuffer.Clear()
	t.ticker.Stop()
}

func (t *EncodedAudioTrack) Close() {
	firstClose := t.closed.CompareAndSwap(false, true)
	// avoid closing the writer multiple times
	if firstClose {
		t.resampledPCMWriter.Close()
		t.pcmWriter.Close()
		t.opusWriter.Close()
	}

}

type DecodedAudioTrack struct {
	*webrtc.TrackRemote
	channels   int
	sampleRate int
	once       sync.Once

	opusWriter         audio.WriteCloser[opus.Sample]
	pcmMWriter         audio.WriteCloser[audio.PCM16Sample]
	resampledPCMWriter audio.WriteCloser[audio.PCM16Sample]
	logger             protoLogger.Logger
}

// TODO: fix channel messiness
// TODO: test stereo with resampler
func NewDecodedAudioTrack(track *webrtc.TrackRemote, targetChannels int, writer *audio.WriteCloser[audio.PCM16Sample], targetSampleRate int, handleJitter bool) (*DecodedAudioTrack, error) {
	if track.Codec().MimeType != webrtc.MimeTypeOpus {
		return nil, errors.New("track is not opus")
	}

	outputChannels := targetChannels
	sourceChannels := DetermineOpusChannels(track)
	if targetChannels > sourceChannels {
		outputChannels = sourceChannels
	}

	resampledPCMWriter := *writer
	if targetSampleRate != DefaultOpusSampleRate {
		resampledPCMWriter = audio.ResampleWriter(*writer, targetSampleRate)
	}

	// opus writer takes opus samples, decodes them to PCM16 samples
	// and writes them to the pcmMWriter
	opusWriter, err := opus.Decode(resampledPCMWriter, outputChannels, protoLogger.GetLogger())
	if err != nil {
		return nil, err
	}

	t := &DecodedAudioTrack{
		TrackRemote:        track,
		opusWriter:         opusWriter,
		pcmMWriter:         *writer,
		resampledPCMWriter: resampledPCMWriter,
		sampleRate:         targetSampleRate,
		channels:           outputChannels,
		logger:             protoLogger.GetLogger(),
	}
	go t.process(handleJitter)
	return t, nil
}

func (t *DecodedAudioTrack) process(handleJitter bool) {
	// Handler takes RTP packets and writes the payload to opusWriter
	var h rtp.Handler = rtp.NewMediaStreamIn[opus.Sample](t.opusWriter)
	if handleJitter {
		h = rtp.HandleJitter(int(t.TrackRemote.Codec().ClockRate), h)
	}

	// HandleLoop takes RTP packets from the track and writes them to the handler
	err := rtp.HandleLoop(t.TrackRemote, h)
	if err != nil && !errors.Is(err, io.EOF) {
		t.logger.Errorw("error handling rtp from track", err)
	}
}

func (t *DecodedAudioTrack) Channels() int {
	return t.channels
}

func (t *DecodedAudioTrack) SampleRate() int {
	return t.sampleRate
}

func (t *DecodedAudioTrack) Close() {
	// opus writer closes resampledPCMWriter internally
	t.opusWriter.Close()
}

// ------------------------------------------------------------------

func splitPCM16SampleToTargetDuration(samples audio.PCM16Sample, sampleRate int, channels int, targetDuration time.Duration) []pcmChunk {
	if len(samples) == 0 || sampleRate <= 0 || channels <= 0 || targetDuration <= 0 {
		return nil
	}

	samplesPerChunk := (sampleRate * channels * int(targetDuration/time.Nanosecond)) / 1e9
	if samplesPerChunk == 0 {
		samplesPerChunk = 1
	}

	totalChunks := len(samples) / samplesPerChunk
	if len(samples)%samplesPerChunk > 0 {
		totalChunks++
	}

	chunks := make([]pcmChunk, 0, totalChunks)
	for i := 0; i < totalChunks; i++ {
		startIdx := i * samplesPerChunk
		endIdx := startIdx + samplesPerChunk
		// TODO: fix this, maybe use 2.5ms target duration?
		// Opus can encode frames of 2.5, 5, 10, 20, 40, or 60 ms
		// so we need to make sure the last chunk is the correct duration
		if endIdx > len(samples) {
			endIdx = len(samples)
		}

		chunk := pcmChunk{
			sample:        make(audio.PCM16Sample, endIdx-startIdx),
			frameDuration: time.Duration(float64(endIdx-startIdx)/float64(sampleRate*channels)*1e9) * time.Nanosecond,
		}

		copy(chunk.sample, samples[startIdx:endIdx])
		chunks = append(chunks, chunk)
		// if chunk.frameDuration < targetDuration {
		// 	missingDuration := targetDuration - chunk.frameDuration
		// 	fmt.Println("missingDuration", missingDuration)
		// 	silentChunk := generateSilentPCM16Chunk(sampleRate, channels, missingDuration)
		// 	chunks = append(chunks, silentChunk)
		// }
	}

	// fmt.Println("chunks", len(chunks), "samples", len(samples), "samplesPerChunk", samplesPerChunk)
	return chunks
}

func generateSilentPCM16Chunk(sampleRate int, channels int, duration time.Duration) pcmChunk {
	numSamples := (sampleRate * channels * int(duration/time.Nanosecond)) / 1e9
	if numSamples == 0 {
		numSamples = 1
	}
	return pcmChunk{
		sample:        make(audio.PCM16Sample, numSamples),
		frameDuration: duration,
	}
}

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

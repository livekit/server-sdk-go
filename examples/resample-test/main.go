package main

import (
	"encoding/binary"
	"os"

	media "github.com/livekit/media-sdk"
	"github.com/livekit/media-sdk/res"
	"github.com/livekit/media-sdk/res/testdata"
)

func main() {
	inputPcm := res.ReadOggAudioFile(testdata.TestAudioOgg, 48000)

	buf := make([]media.PCM16Sample, 0, 10000)
	writer := media.NewPCM16FrameWriter(&buf, 24000)
	resampler := media.ResampleWriter(writer, 48000)

	for _, sample := range inputPcm {
		resampler.WriteSample(sample)
	}

	file, err := os.Create("output.pcm")
	if err != nil {
		panic(err)
	}
	defer file.Close()

	for _, sample := range buf {
		sampleBytes := make([]byte, len(sample)*2)
		for i := 0; i < len(sample); i++ {
			binary.LittleEndian.PutUint16(sampleBytes[i*2:], uint16(sample[i]))
		}
		file.Write(sampleBytes)
	}
}

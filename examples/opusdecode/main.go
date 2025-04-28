package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"

	_ "embed"

	mediaopus "github.com/livekit/media-sdk/opus"
	webm "github.com/livekit/media-sdk/webm"
	"github.com/livekit/protocol/logger"
	"github.com/pion/opus/pkg/oggreader"

	lksdk "github.com/livekit/server-sdk-go/v2"
)

//go:embed input.opus
var opusAudio []byte

func main() {
	outputFile, err := os.Create("output.mka")
	if err != nil {
		panic(err)
	}
	defer outputFile.Close()

	opusSamples := readOpusFile(opusAudio)
	fmt.Printf("Found %d opus packets\n", len(opusSamples))
	pcmWriter := webm.NewPCM16Writer(outputFile, 48000, 2, lksdk.DefaultOpusSampleDuration)
	opusWriter, err := mediaopus.Decode(pcmWriter, 2, logger.GetLogger())
	if err != nil {
		panic(err)
	}
	defer opusWriter.Close()

	for _, sample := range opusSamples {
		err := opusWriter.WriteSample(sample)
		if err != nil {
			panic(err)
		}
	}
}

func readOpusFile(data []byte) []mediaopus.Sample {
	oggReader, _, err := oggreader.NewWith(bytes.NewReader(data))
	if err != nil {
		panic(err)
	}

	var samples []mediaopus.Sample

	for {
		segments, _, err := oggReader.ParseNextPage()

		if errors.Is(err, io.EOF) {
			break
		} else if bytes.HasPrefix(segments[0], []byte("OpusTags")) {
			continue
		}

		if err != nil {
			panic(err)
		}

		for _, segment := range segments {
			if len(segment) > 0 {
				samples = append(samples, mediaopus.Sample(segment))
			}
		}
	}

	return samples
}

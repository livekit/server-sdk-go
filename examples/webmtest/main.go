package main

import (
	_ "embed"
	"os"

	lksdk "github.com/livekit/server-sdk-go/v2"

	"github.com/livekit/media-sdk/res"
	"github.com/livekit/media-sdk/webm"
)

//go:embed stereo.ogg
var stereoOgg []byte

func main() {
	outputFile, err := os.Create("stereo2.mka")
	if err != nil {
		panic(err)
	}
	defer outputFile.Close()

	audio := res.ReadOggAudioFile(stereoOgg, 44100, 2)
	webmWriter := webm.NewPCM16Writer(outputFile, 44100, 2, lksdk.DefaultOpusSampleDuration)

	for _, sample := range audio {
		err := webmWriter.WriteSample(sample)
		if err != nil {
			panic(err)
		}
	}
}

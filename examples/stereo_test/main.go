package main

import (
	"bytes"
	_ "embed"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	media "github.com/livekit/media-sdk"
	opus "github.com/livekit/media-sdk/opus"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/server-sdk-go/v2/pkg/oggreader"
)

//go:embed stereo.opus
var stereoOpus []byte

var totalDuration = 0 * time.Nanosecond
var totalPCMDuration = 0 * time.Nanosecond

type PCM16Writer struct {
	file       *os.File
	sampleRate int
}

func NewPCM16Writer(file *os.File, sampleRate int) *PCM16Writer {
	return &PCM16Writer{
		file:       file,
		sampleRate: sampleRate,
	}
}

func (w *PCM16Writer) WriteSample(sample media.PCM16Sample) error {
	sampleBytes := make([]byte, len(sample)*2)
	for i := 0; i < len(sample); i++ {
		binary.LittleEndian.PutUint16(sampleBytes[i*2:], uint16(sample[i]))
	}

	totalPCMDuration += time.Duration(len(sample)) * time.Second / time.Duration(w.sampleRate)

	_, err := w.file.Write(sampleBytes)
	return err
}

func (w *PCM16Writer) SampleRate() int {
	return w.sampleRate
}

func (w *PCM16Writer) String() string {
	return "PCM16Writer"
}

func (w *PCM16Writer) Close() error {
	return w.file.Close()
}

func processOggOpus(reader io.Reader, opusDecoder media.WriteCloser[opus.Sample]) error {
	oggReader, header, err := oggreader.NewOggReader(reader)
	if err != nil {
		return fmt.Errorf("failed to create ogg reader: %w", err)
	}

	fmt.Printf("Ogg header info: Channels: %d, SampleRate: %d\n",
		header.Channels, header.SampleRate)

	for {
		packet, err := oggReader.ReadPacket()
		exit := false

		if err != nil {
			if errors.Is(err, io.EOF) {
				exit = true
			} else {
				return fmt.Errorf("failed to read ogg packet: %w", err)
			}
		}

		if len(packet) > 0 {
			duration, err := oggreader.ParsePacketDuration(packet)
			if err != nil {
				return fmt.Errorf("failed to parse ogg packet duration: %w", err)
			}
			totalDuration += duration
			err = opusDecoder.WriteSample(opus.Sample(packet))
			if err != nil {
				return fmt.Errorf("failed to write opus packet: %w", err)
			}
		}

		if exit {
			break
		}
	}

	return nil
}

func main() {
	log := logger.GetLogger()

	pcmFile, err := os.Create("output.pcm")
	if err != nil {
		fmt.Printf("Error creating output file: %v\n", err)
		return
	}
	defer pcmFile.Close()

	pcmWriter := NewPCM16Writer(pcmFile, 48000)
	opusDecoder, err := opus.Decode(pcmWriter, 2, log)
	if err != nil {
		fmt.Printf("Error creating opus decoder: %v\n", err)
		return
	}
	defer opusDecoder.Close()

	err = processOggOpus(bytes.NewReader(stereoOpus), opusDecoder)
	if err != nil {
		fmt.Printf("Error processing Ogg opus: %v\n", err)
		return
	}
	fmt.Printf("Total duration: %v\n", totalDuration)
	fmt.Printf("Total PCM duration: %v\n", totalPCMDuration)
}

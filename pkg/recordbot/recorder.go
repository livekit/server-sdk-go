package recordbot

import (
	"errors"
	"log"
	"sync"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/pion/webrtc/v3/pkg/media/h264writer"
	"github.com/pion/webrtc/v3/pkg/media/ivfwriter"
	"github.com/pion/webrtc/v3/pkg/media/oggwriter"
)

var ErrUnsupportedCodec = errors.New("unsupported codec")

type Recorder interface {
	// Reads RTP packets in TrackRemote and save them to local file.
	// No need to call this in a goroutine as the function already calls one.
	Start(track *webrtc.TrackRemote)

	// Stops the recording and does cleanups such as closing the file and channel.
	Stop()
}

type recorder struct {
	// Save file name to be passed into hooks
	filename string

	// Each recorder uses different writer depending if it's for video / audio
	writer media.Writer

	// The `done` channel is an internal channel for tracking the start to finish of the recording
	done chan bool

	// WaitGroup is used to ensure everything is cleaned up in the goroutines before closing the recorder
	wg sync.WaitGroup

	// Hooks provide access to customising aspects of the recorder, such as uploading the file after recording
	hooks RecorderHooks
}

type RecorderHooks struct {
	// The recorder will pass the filename that it was instantiated with, to the hook
	UploadFile func(filename string) error
}

func NewRecorder(filename string, codec webrtc.RTPCodecParameters, hooks RecorderHooks) (Recorder, error) {
	// Instantiates the appropriate media writer to save RTP packets
	writer, err := createMediaWriter(filename, codec.MimeType)
	if err != nil {
		return nil, err
	}

	// Make channel for tracking recorder state
	done := make(chan bool, 1)

	return &recorder{filename, writer, done, sync.WaitGroup{}, hooks}, nil
}

func createMediaWriter(fileName string, mimeType string) (media.Writer, error) {
	if mimeType == webrtc.MimeTypeVP8 || mimeType == webrtc.MimeTypeVP9 {
		return ivfwriter.New(fileName)
	}
	if mimeType == webrtc.MimeTypeH264 {
		return h264writer.New(fileName)
	}
	if mimeType == webrtc.MimeTypeG722 ||
		mimeType == webrtc.MimeTypePCMA ||
		mimeType == webrtc.MimeTypePCMU ||
		mimeType == webrtc.MimeTypeOpus {
		// The values for oggwriter can be found at
		// https://github.com/pion/webrtc/blob/master/examples/save-to-disk/main.go#L53
		return oggwriter.New(fileName, 48000, 0)
	}
	return nil, ErrUnsupportedCodec
}

func (r *recorder) Start(track *webrtc.TrackRemote) {
	r.wg.Add(1)
	go r.record(track)
}

func (r *recorder) record(track *webrtc.TrackRemote) {
	// Run forever loop to save the packets to file, until we receive a stop signal
	for {
		select {
		case done := <-r.done:
			if done {
				r.cleanup()

				// If we specified file upload, run it in a goroutine
				if r.hooks.UploadFile != nil {
					r.wg.Add(1)
					go func() {
						err := r.hooks.UploadFile(r.filename)
						if err != nil {
							log.Fatal(err)
						}
						// Once we're done uploading, signal that we've finished the job
						r.wg.Done()
					}()
				}

				// Signal all jobs are finished
				r.wg.Done()
			}
		default:
			err := writeTrackToFile(r.writer, track)
			if err != nil {
				// Do something with error before exiting
				r.cleanup()
			}
		}
	}
}

func writeTrackToFile(writer media.Writer, track *webrtc.TrackRemote) error {
	// If track is nil, return
	if track == nil {
		return errors.New("track is nil")
	}

	// Read RTP stream
	packet, _, err := track.ReadRTP()
	if err != nil {
		return err
	}

	// Write to file
	err = writer.WriteRTP(packet)
	if err != nil {
		return err
	}

	// Return success
	return nil
}

func (r *recorder) cleanup() error {
	// Close file
	err := r.writer.Close()
	if err != nil {
		return err
	}

	// Close the channel
	close(r.done)
	return nil
}

func (r *recorder) Stop() {
	// Signal recorder to stop
	r.done <- true

	// Wait for goroutine to finish cleaning up before returning
	r.wg.Wait()
}

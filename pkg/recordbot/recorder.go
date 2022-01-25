package recordbot

import (
	"errors"
	"log"

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

	// Channels for tracking the state of the recorder
	done   chan struct{}
	closed chan struct{}

	// Hooks provide access to customising aspects of the recorder, such as uploading the file after recording
	hooks RecorderHooks
}

type RecorderHooks struct {
	// The recorder will pass the filename that it was instantiated with, to the hook
	UploadFile func(filename string) error
}

func NewSingleTrackRecorder(filename string, codec webrtc.RTPCodecParameters, hooks RecorderHooks) (Recorder, error) {
	// Instantiates the appropriate media writer to save RTP packets
	writer, err := createMediaWriter(filename, codec.MimeType)
	if err != nil {
		return nil, err
	}

	// Make channels for tracking recorder state
	done := make(chan struct{}, 1)
	closed := make(chan struct{}, 1)

	return &recorder{filename, writer, done, closed, hooks}, nil
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
	go r.record(track)
}

func (r *recorder) record(track *webrtc.TrackRemote) {
	var err error
	defer func() {
		// Handle error during recording
		if err != nil {
			log.Println(err)
		}

		// Close file
		err := r.writer.Close()
		if err != nil {
			log.Println(err)
		}

		// Upload file if the hook was provided
		if r.hooks.UploadFile != nil {
			err := r.hooks.UploadFile(r.filename)
			if err != nil {
				log.Println(err)
			}
		}

		// Signal all jobs are finished (this unblocks the execution in Stop())
		close(r.closed)
	}()

	// Run forever loop to save the packets to file, until we receive a stop signal
	for {
		select {
		case <-r.done:
			return
		default:
			// Read RTP stream
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}

			// Write to file
			err = r.writer.WriteRTP(packet)
			if err != nil {
				return
			}
		}
	}
}

func (r *recorder) Stop() {
	// Close sends a signal to `done` channel, stopping the recorder
	close(r.done)

	// Block execution until post processing is done
	<-r.closed
}

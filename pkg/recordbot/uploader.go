package recordbot

import "io"

type Uploader interface {
	// Key is a unique identifier for the file. This doesn't have to be the file name;
	// it could include a prefix to indicate upload to a directory (e.g. "livekit/myfile.mp4")
	Upload(key string, body io.Reader) error
}

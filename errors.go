package lksdk

import "errors"

var (
	ErrConnectionTimeout   = errors.New("could not connect after timeout")
	ErrTrackPublishTimeout = errors.New("timed out publishing track")
	ErrCannotDetermineMime = errors.New("cannot determine mimetype from file extension")
	ErrUnsupportedFileType = errors.New("FileSampleProvider does not support this mime type")
)

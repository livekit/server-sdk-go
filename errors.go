package lksdk

import "errors"

var (
	ErrConnectionTimeout   = errors.New("could not connect after timeout")
	ErrTrackPublishTimeout = errors.New("timed out publishing track")
)

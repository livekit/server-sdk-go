package sdk

import "errors"

var (
	ErrConnectionTimeout = errors.New("could not connect after timeout")
)

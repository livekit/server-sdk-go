package lksdk

import (
	"github.com/go-logr/logr"
)

var logger = logr.Discard()

func SetLogger(l logr.Logger) {
	logger = l
}

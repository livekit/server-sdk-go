package lksdk

import (
	"log"

	"github.com/go-logr/stdr"

	protoLogger "github.com/livekit/protocol/logger"
)

var logger protoLogger.Logger = protoLogger.LogRLogger(stdr.New(log.Default()))

// SetLogger overrides default logger. To use a [logr](https://github.com/go-logr/logr) compatible logger,
// pass in SetLogger(logger.LogRLogger(logRLogger))
func SetLogger(l protoLogger.Logger) {
	logger = l
}

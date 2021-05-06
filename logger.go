package lksdk

import (
	"log"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
)

var logger = stdr.New(log.Default())

// SetLogger overrides logger with a logr implementation
// https://github.com/go-logr/logr
func SetLogger(l logr.Logger) {
	logger = l
}

package jitter

import "github.com/livekit/protocol/logger"

type Option func(b *Buffer)

// WithPacketDroppedHandler sets a callback that's called when a packet
// is dropped. This signifies packet loss.
func WithPacketDroppedHandler(f func()) Option {
	return func(b *Buffer) {
		b.onPacketDropped = f
	}
}

// WithLogger sets a logger which will log packets dropped
func WithLogger(l logger.Logger) Option {
	return func(b *Buffer) {
		b.logger = l
	}
}

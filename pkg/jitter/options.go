package jitter

import "github.com/livekit/protocol/logger"

// An Option configures a SampleBuilder
type Option func(b *Buffer)

// WithPacketDroppedHandler sets a callback that's called when a packet
// is dropped. This signifies packet loss.
func WithPacketDroppedHandler(f func()) Option {
	return func(b *Buffer) {
		b.onPacketDropped = f
	}
}

func WithLogger(l logger.Logger) Option {
	return func(b *Buffer) {
		b.logger = l
	}
}

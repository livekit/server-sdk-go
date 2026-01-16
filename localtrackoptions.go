package lksdk

import "github.com/pion/webrtc/v4"

type TrackLocalWithCodec interface {
	webrtc.TrackLocal
	Codec() webrtc.RTPCodecCapability
}

type LocalTrackPublishOptions struct {
	backupCodecTrack TrackLocalWithCodec
	// backup codec tracks for simulcast track
	backupCodecTracks []*LocalTrack
	restrictCodec     bool
	codecPreference   webrtc.RTPCodecCapability
}

type LocalTrackPublishOption func(*LocalTrackPublishOptions)

func WithBackupCodec(backupCodecTrack TrackLocalWithCodec) LocalTrackPublishOption {
	return func(opts *LocalTrackPublishOptions) {
		opts.backupCodecTrack = backupCodecTrack
	}
}

func WithBackupCodecForSimulcastTrack(backupCodecTracks []*LocalTrack) LocalTrackPublishOption {
	return func(opts *LocalTrackPublishOptions) {
		opts.backupCodecTracks = backupCodecTracks
	}
}

// WithCodec restricts the advertised codec list to the provided codec.
// If codec.MimeType is empty, this is a no-op.
func WithCodec(codec webrtc.RTPCodecCapability) LocalTrackPublishOption {
	return func(opts *LocalTrackPublishOptions) {
		if codec.MimeType == "" {
			return
		}
		opts.restrictCodec = true
		opts.codecPreference = codec
	}
}

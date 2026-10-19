// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package datatrack is the public API for data tracks.
package datatrack

import (
	backend "github.com/livekit/server-sdk-go/v2/internal/datatrack"
)

type (
	// LocalTrack is a data track published by the local participant.
	LocalTrack = backend.LocalTrack
	// RemoteTrack is a data track published by a remote participant.
	RemoteTrack = backend.RemoteTrack
	// Stream delivers the frames of a subscription.
	Stream = backend.Stream
	// Info describes a published data track.
	Info = backend.Info
	// Frame is a frame published on a data track.
	Frame = backend.Frame

	// PublishOptions describe a data track to publish.
	PublishOptions = backend.PublishOptions
	// PublishOption customizes a publication.
	PublishOption = backend.PublishOption

	// SubscribeOptions configure a subscription.
	SubscribeOptions = backend.SubscribeOptions
	// SubscribeOption customizes a subscription.
	SubscribeOption = backend.SubscribeOption
	// PipelineOptions configure how a remote track's packets are reassembled.
	PipelineOptions = backend.PipelineOptions

	// SchemaID identifies a data track schema.
	SchemaID = backend.SchemaID
	// SchemaEncoding is the encoding of a schema definition: a WellKnownSchemaEncoding or a
	// CustomSchemaEncoding.
	SchemaEncoding = backend.SchemaEncoding
	// WellKnownSchemaEncoding is a schema encoding defined by the protocol.
	WellKnownSchemaEncoding = backend.WellKnownSchemaEncoding
	// CustomSchemaEncoding is an application-defined schema encoding identified by name.
	CustomSchemaEncoding = backend.CustomSchemaEncoding

	// FrameEncoding is the encoding of frames pushed on a data track: a WellKnownFrameEncoding or
	// a CustomFrameEncoding.
	FrameEncoding = backend.FrameEncoding
	// WellKnownFrameEncoding is a frame encoding defined by the protocol.
	WellKnownFrameEncoding = backend.WellKnownFrameEncoding
	// CustomFrameEncoding is an application-defined frame encoding identified by name.
	CustomFrameEncoding = backend.CustomFrameEncoding
)

var (
	ErrNotAllowed       = backend.ErrNotAllowed
	ErrDuplicateName    = backend.ErrDuplicateName
	ErrInvalidName      = backend.ErrInvalidName
	ErrLimitReached     = backend.ErrLimitReached
	ErrPublishTimeout   = backend.ErrPublishTimeout
	ErrSubscribeTimeout = backend.ErrSubscribeTimeout
	ErrDisconnected     = backend.ErrDisconnected
	ErrUnpublished      = backend.ErrUnpublished
	ErrQueueFull        = backend.ErrQueueFull

	ErrSchemaMissingFrameEncoding = backend.ErrSchemaMissingFrameEncoding
	ErrSchemaMissingID            = backend.ErrSchemaMissingID
	ErrSchemaIncompatible         = backend.ErrSchemaIncompatible
)

const (
	// SchemaEncodingOther is a well-known encoding not known to this SDK version.
	SchemaEncodingOther      = backend.SchemaEncodingOther
	SchemaEncodingProtobuf   = backend.SchemaEncodingProtobuf
	SchemaEncodingFlatbuffer = backend.SchemaEncodingFlatbuffer
	SchemaEncodingROS1Msg    = backend.SchemaEncodingROS1Msg
	SchemaEncodingROS2Msg    = backend.SchemaEncodingROS2Msg
	SchemaEncodingROS2IDL    = backend.SchemaEncodingROS2IDL
	SchemaEncodingOMGIDL     = backend.SchemaEncodingOMGIDL
	SchemaEncodingJSONSchema = backend.SchemaEncodingJSONSchema
)

const (
	// FrameEncodingOther is a well-known encoding not known to this SDK version.
	FrameEncodingOther      = backend.FrameEncodingOther
	FrameEncodingROS1       = backend.FrameEncodingROS1
	FrameEncodingCDR        = backend.FrameEncodingCDR
	FrameEncodingProtobuf   = backend.FrameEncodingProtobuf
	FrameEncodingFlatbuffer = backend.FrameEncodingFlatbuffer
	FrameEncodingCBOR       = backend.FrameEncodingCBOR
	FrameEncodingMsgPack    = backend.FrameEncodingMsgPack
	FrameEncodingJSON       = backend.FrameEncodingJSON
)

// WithSchema associates a schema with the track's frames.
func WithSchema(schema SchemaID) PublishOption {
	return backend.WithSchema(schema)
}

// WithFrameEncoding declares the encoding of the track's frames.
func WithFrameEncoding(encoding FrameEncoding) PublishOption {
	return backend.WithFrameEncoding(encoding)
}

// WithBufferSize sets the number of received frames buffered for the subscriber. It has no
// effect when the track already has an active subscription.
func WithBufferSize(frames int) SubscribeOption {
	return backend.WithBufferSize(frames)
}

// UserTimestampNow returns the current Unix time in milliseconds, the interpretation
// Frame.DurationSinceTimestamp expects.
func UserTimestampNow() *uint64 {
	return backend.UserTimestampNow()
}

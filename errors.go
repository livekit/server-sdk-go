// Copyright 2023 LiveKit, Inc.
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

package lksdk

import (
	"errors"

	"github.com/livekit/server-sdk-go/v2/signalling"
)

var (
	ErrConnectionTimeout        = errors.New("could not connect after timeout")
	ErrTrackPublishTimeout      = errors.New("timed out publishing track")
	ErrCannotDetermineMime      = errors.New("cannot determine mimetype from file extension")
	ErrUnsupportedFileType      = errors.New("ReaderSampleProvider does not support this mime type")
	ErrUnsupportedSimulcastKind = errors.New("simulcast is only supported for video")
	ErrInvalidSimulcastTrack    = errors.New("simulcast track was not initiated correctly")
	ErrCannotFindTrack          = errors.New("could not find the track")
	ErrInvalidMessageType       = signalling.ErrInvalidMessageType
	ErrInvalidParameter         = signalling.ErrInvalidParameter
	ErrNoPeerConnection         = errors.New("peer connection not established")
	ErrAborted                  = errors.New("operation was aborted")
)

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
	"log/slog"

	protoLogger "github.com/livekit/protocol/logger"
)

var globalLog *slog.Logger

func getLogger() *slog.Logger {
	if globalLog != nil {
		return globalLog
	}
	return slog.Default()
}

// SetLogger overrides default logger. To use a [logr](https://github.com/go-logr/logr) compatible logger,
// pass in SetLogger(logger.LogRLogger(logRLogger)).
//
// If no logger is set, slog.Default will be used.
func SetLogger(l protoLogger.Logger) {
	if l == nil {
		globalLog = nil
		return
	}
	globalLog = slog.New(protoLogger.ToSlogHandler(l))
}

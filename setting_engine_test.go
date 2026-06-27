// Copyright 2024 LiveKit, Inc.
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
	"testing"

	"github.com/pion/webrtc/v4"

	"github.com/livekit/server-sdk-go/v2/signalling"
)

func TestWithSettingEngineFunc(t *testing.T) {
	p := &connParams{ConnectParams: &signalling.ConnectParams{}}

	called := false
	WithSettingEngineFunc(func(se *webrtc.SettingEngine) { called = true })(p)

	if p.SettingEngineFunc == nil {
		t.Fatal("WithSettingEngineFunc must set ConnectParams.SettingEngineFunc")
	}

	p.SettingEngineFunc(&webrtc.SettingEngine{})
	if !called {
		t.Fatal("the stored func must be the one supplied to WithSettingEngineFunc")
	}
}

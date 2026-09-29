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
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		v1, v2 string
		want   int
	}{
		{"1.0.0", "1.0.0", 0},
		{"2.0.0", "1.0.0", 1},
		{"1.0.0", "2.0.0", -1},
		{"1.2.3", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.2.3", "1.2.4", -1},
		// double-digit components — would fail with string comparison
		{"10.0.0", "9.0.0", 1},
		{"1.10.0", "1.9.0", 1},
		{"1.0.10", "1.0.9", 1},
		// trailing zeros are treated as equal
		{"1.0", "1.0.0", 0},
		{"1.0.0", "1.0", 0},
		// different length where values differ
		{"1.1", "1.0.9", 1},
		{"1.0", "1.1.0", -1},
	}

	for _, tc := range cases {
		got := compareVersions(tc.v1, tc.v2)
		if got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.v1, tc.v2, got, tc.want)
		}
	}
}

func TestTruncateBytes(t *testing.T) {
	cases := []struct {
		name     string
		str      string
		maxBytes int
		want     string
	}{
		{"shorter than limit", "abc", 5, "abc"},
		{"exactly at limit", "abc", 3, "abc"},
		{"ascii over limit", "abcdef", 3, "abc"},
		{"multibyte on boundary", "aé", 3, "aé"},
		{"cut inside 2-byte rune", "aéb", 2, "a"},
		{"cut inside 3-byte rune", "a€", 3, "a"},
		{"cut inside 4-byte rune", "a😀", 4, "a"},
		{"limit smaller than first rune", "é", 1, ""},
		{"invalid byte within limit", "ba\xffd", 10, "bad"},
		{"invalid byte before the cut", "ab\xffcdef", 4, "abcd"},
		{"invalid byte after the cut", "abcdef\xff", 3, "abc"},
		{"truncated sequence at the end", "ab\xe2\x82", 10, "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateBytes(tc.str, tc.maxBytes)
			if got != tc.want {
				t.Errorf("truncateBytes(%q, %d) = %q, want %q", tc.str, tc.maxBytes, got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncateBytes(%q, %d) = %q is not valid UTF-8", tc.str, tc.maxBytes, got)
			}
		})
	}
}

func TestNewRpcErrorTruncatesToValidUTF8(t *testing.T) {
	// The leading ASCII byte shifts the 2-byte runes so the 256 byte limit falls inside one.
	msg := "a" + strings.Repeat("é", 200)
	err := NewRpcError(RpcApplicationError, msg, nil)
	if _, mErr := proto.Marshal(err.toProto()); mErr != nil {
		t.Fatalf("marshal truncated RpcError: %v", mErr)
	}
	if len(err.Message) > MaxMessageBytes {
		t.Errorf("message is %d bytes, want <= %d", len(err.Message), MaxMessageBytes)
	}
}

func TestNewRpcErrorDropsMalformedUTF8(t *testing.T) {
	data := "d\xffata"
	err := NewRpcError(RpcApplicationError, "bad\xff", &data)
	if _, mErr := proto.Marshal(err.toProto()); mErr != nil {
		t.Fatalf("marshal RpcError with malformed text: %v", mErr)
	}
	if err.Message != "bad" || *err.Data != "data" {
		t.Errorf("got message %q and data %q, want %q and %q", err.Message, *err.Data, "bad", "data")
	}
}

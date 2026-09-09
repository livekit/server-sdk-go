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

package datatrack

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateSchema_NotSpecified(t *testing.T) {
	require.NoError(t, validateSchema(nil, nil))
}

func TestValidateSchema_SelfDescribing(t *testing.T) {
	require.NoError(t, validateSchema(FrameEncodingJSON, nil))
}

func TestValidateSchema_CompatibleEncodings(t *testing.T) {
	require.NoError(t, validateSchema(FrameEncodingCDR, SchemaEncodingROS2IDL))
}

func TestValidateSchema_Custom(t *testing.T) {
	require.NoError(t, validateSchema(
		CustomFrameEncoding("my-frame-encoding"),
		CustomSchemaEncoding("my-schema-encoding"),
	))
}

func TestValidateSchema_MissingFrameEncoding(t *testing.T) {
	require.ErrorIs(t, validateSchema(nil, SchemaEncodingProtobuf), ErrSchemaMissingFrameEncoding)
}

func TestValidateSchema_MissingSchemaID(t *testing.T) {
	require.ErrorIs(t, validateSchema(FrameEncodingProtobuf, nil), ErrSchemaMissingID)
}

func TestValidateSchema_Incompatible(t *testing.T) {
	require.ErrorIs(t, validateSchema(FrameEncodingJSON, SchemaEncodingProtobuf), ErrSchemaIncompatible)
}

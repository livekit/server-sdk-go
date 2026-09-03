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
	"errors"

	"github.com/livekit/protocol/livekit"
)

var (
	ErrSchemaMissingFrameEncoding = errors.New("frame encoding is required when providing schema ID")
	ErrSchemaMissingID            = errors.New("schema ID is required for frame encoding that is not self-describing")
	ErrSchemaIncompatible         = errors.New("specified schema and frame encodings are incompatible")
)

// SchemaID identifies a data track schema. IDs with the same name but different encodings are distinct.
type SchemaID struct {
	Name     string
	Encoding SchemaEncoding
}

// SchemaEncoding is the encoding of a schema definition. The zero value is SchemaEncodingOther.
type SchemaEncoding struct {
	wellKnown livekit.DataTrackSchemaEncoding_WellKnownSchemaEncoding
	custom    string
}

var (
	SchemaEncodingProtobuf   = SchemaEncoding{wellKnown: livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_PROTOBUF}
	SchemaEncodingFlatbuffer = SchemaEncoding{wellKnown: livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_FLATBUFFER}
	SchemaEncodingROS1Msg    = SchemaEncoding{wellKnown: livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_ROS1_MSG}
	SchemaEncodingROS2Msg    = SchemaEncoding{wellKnown: livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_ROS2_MSG}
	SchemaEncodingROS2IDL    = SchemaEncoding{wellKnown: livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_ROS2_IDL}
	SchemaEncodingOMGIDL     = SchemaEncoding{wellKnown: livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_OMG_IDL}
	SchemaEncodingJSONSchema = SchemaEncoding{wellKnown: livekit.DataTrackSchemaEncoding_WELL_KNOWN_SCHEMA_ENCODING_JSON_SCHEMA}
	// SchemaEncodingOther is a well-known encoding not known to this SDK version.
	SchemaEncodingOther = SchemaEncoding{}
)

var schemaEncodingNames = map[livekit.DataTrackSchemaEncoding_WellKnownSchemaEncoding]string{
	SchemaEncodingProtobuf.wellKnown:   "protobuf",
	SchemaEncodingFlatbuffer.wellKnown: "flatbuffer",
	SchemaEncodingROS1Msg.wellKnown:    "ros1msg",
	SchemaEncodingROS2Msg.wellKnown:    "ros2msg",
	SchemaEncodingROS2IDL.wellKnown:    "ros2idl",
	SchemaEncodingOMGIDL.wellKnown:     "omgidl",
	SchemaEncodingJSONSchema.wellKnown: "jsonschema",
}

// CustomSchemaEncoding is an application-specific schema encoding identified by name.
func CustomSchemaEncoding(name string) SchemaEncoding {
	return SchemaEncoding{custom: name}
}

// Custom returns the identifier of a custom encoding.
func (e SchemaEncoding) Custom() (string, bool) {
	return e.custom, e.custom != ""
}

func (e SchemaEncoding) String() string {
	if e.custom != "" {
		return e.custom
	}
	if name, ok := schemaEncodingNames[e.wellKnown]; ok {
		return name
	}
	return "other"
}

func schemaEncodingFromProto(msg *livekit.DataTrackSchemaEncoding) SchemaEncoding {
	switch value := msg.GetValue().(type) {
	case *livekit.DataTrackSchemaEncoding_WellKnown:
		if _, known := schemaEncodingNames[value.WellKnown]; known {
			return SchemaEncoding{wellKnown: value.WellKnown}
		}
		return SchemaEncodingOther
	case *livekit.DataTrackSchemaEncoding_Custom:
		return CustomSchemaEncoding(value.Custom)
	default:
		return SchemaEncodingOther
	}
}

func (e SchemaEncoding) toProto() *livekit.DataTrackSchemaEncoding {
	if e.custom != "" {
		return &livekit.DataTrackSchemaEncoding{Value: &livekit.DataTrackSchemaEncoding_Custom{Custom: e.custom}}
	}
	return &livekit.DataTrackSchemaEncoding{Value: &livekit.DataTrackSchemaEncoding_WellKnown{WellKnown: e.wellKnown}}
}

func schemaIDFromProto(msg *livekit.DataTrackSchemaId) SchemaID {
	id := SchemaID{Name: msg.GetName(), Encoding: SchemaEncodingOther}
	if msg.GetEncoding() != nil {
		id.Encoding = schemaEncodingFromProto(msg.GetEncoding())
	}
	return id
}

func (id SchemaID) toProto() *livekit.DataTrackSchemaId {
	return &livekit.DataTrackSchemaId{Name: id.Name, Encoding: id.Encoding.toProto()}
}

// FrameEncoding is the encoding of frames pushed on a data track. The zero value is FrameEncodingOther.
type FrameEncoding struct {
	wellKnown livekit.DataTrackFrameEncoding_WellKnownFrameEncoding
	custom    string
}

var (
	FrameEncodingROS1       = FrameEncoding{wellKnown: livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_ROS1}
	FrameEncodingCDR        = FrameEncoding{wellKnown: livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_CDR}
	FrameEncodingProtobuf   = FrameEncoding{wellKnown: livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_PROTOBUF}
	FrameEncodingFlatbuffer = FrameEncoding{wellKnown: livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_FLATBUFFER}
	FrameEncodingCBOR       = FrameEncoding{wellKnown: livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_CBOR}
	FrameEncodingMsgPack    = FrameEncoding{wellKnown: livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_MSGPACK}
	FrameEncodingJSON       = FrameEncoding{wellKnown: livekit.DataTrackFrameEncoding_WELL_KNOWN_FRAME_ENCODING_JSON}
	// FrameEncodingOther is a well-known encoding not known to this SDK version.
	FrameEncodingOther = FrameEncoding{}
)

var frameEncodingNames = map[livekit.DataTrackFrameEncoding_WellKnownFrameEncoding]string{
	FrameEncodingROS1.wellKnown:       "ros1",
	FrameEncodingCDR.wellKnown:        "cdr",
	FrameEncodingProtobuf.wellKnown:   "protobuf",
	FrameEncodingFlatbuffer.wellKnown: "flatbuffer",
	FrameEncodingCBOR.wellKnown:       "cbor",
	FrameEncodingMsgPack.wellKnown:    "msgpack",
	FrameEncodingJSON.wellKnown:       "json",
}

// CustomFrameEncoding is an application-specific frame encoding identified by name.
func CustomFrameEncoding(name string) FrameEncoding {
	return FrameEncoding{custom: name}
}

// Custom returns the identifier of a custom encoding.
func (e FrameEncoding) Custom() (string, bool) {
	return e.custom, e.custom != ""
}

func (e FrameEncoding) String() string {
	if e.custom != "" {
		return e.custom
	}
	if name, ok := frameEncodingNames[e.wellKnown]; ok {
		return name
	}
	return "other"
}

func frameEncodingFromProto(msg *livekit.DataTrackFrameEncoding) FrameEncoding {
	switch value := msg.GetValue().(type) {
	case *livekit.DataTrackFrameEncoding_WellKnown:
		if _, known := frameEncodingNames[value.WellKnown]; known {
			return FrameEncoding{wellKnown: value.WellKnown}
		}
		return FrameEncodingOther
	case *livekit.DataTrackFrameEncoding_Custom:
		return CustomFrameEncoding(value.Custom)
	default:
		return FrameEncodingOther
	}
}

func (e FrameEncoding) toProto() *livekit.DataTrackFrameEncoding {
	if e.custom != "" {
		return &livekit.DataTrackFrameEncoding{Value: &livekit.DataTrackFrameEncoding_Custom{Custom: e.custom}}
	}
	return &livekit.DataTrackFrameEncoding{Value: &livekit.DataTrackFrameEncoding_WellKnown{WellKnown: e.wellKnown}}
}

// isSelfDescribing reports whether frames need no schema; ok is false when this cannot be determined.
func (e FrameEncoding) isSelfDescribing() (selfDescribing, ok bool) {
	switch e {
	case FrameEncodingCBOR, FrameEncodingMsgPack, FrameEncodingJSON:
		return true, true
	case FrameEncodingOther:
		return false, false
	}
	if e.custom != "" {
		return false, false
	}
	return false, true
}

// isDescribedBy reports whether a schema with the given encoding can describe frames with this
// encoding; ok is false when this cannot be determined.
func (e FrameEncoding) isDescribedBy(schema SchemaEncoding) (described, ok bool) {
	if e == FrameEncodingOther || e.custom != "" {
		return false, false
	}
	switch {
	case e == FrameEncodingROS1 && schema == SchemaEncodingROS1Msg,
		e == FrameEncodingCDR && schema == SchemaEncodingROS2Msg,
		e == FrameEncodingCDR && schema == SchemaEncodingROS2IDL,
		e == FrameEncodingCDR && schema == SchemaEncodingOMGIDL,
		e == FrameEncodingProtobuf && schema == SchemaEncodingProtobuf,
		e == FrameEncodingFlatbuffer && schema == SchemaEncodingFlatbuffer,
		e == FrameEncodingJSON && schema == SchemaEncodingJSONSchema:
		return true, true
	}
	return false, true
}

// validateSchema checks that the given frame and schema encodings are compatible.
func validateSchema(frameEncoding *FrameEncoding, schemaEncoding *SchemaEncoding) error {
	switch {
	case frameEncoding == nil && schemaEncoding != nil:
		return ErrSchemaMissingFrameEncoding
	case frameEncoding != nil && schemaEncoding == nil:
		if selfDescribing, ok := frameEncoding.isSelfDescribing(); ok && !selfDescribing {
			return ErrSchemaMissingID
		}
	case frameEncoding != nil && schemaEncoding != nil:
		if described, ok := frameEncoding.isDescribedBy(*schemaEncoding); ok && !described {
			return ErrSchemaIncompatible
		}
	}
	return nil
}

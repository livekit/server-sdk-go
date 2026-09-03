// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package datatrack

import (
	"errors"
	"fmt"
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

// SchemaEncoding is the encoding of a schema definition: a WellKnownSchemaEncoding or a
// CustomSchemaEncoding.
type SchemaEncoding interface {
	fmt.Stringer
	isSchemaEncoding()
}

// WellKnownSchemaEncoding is a schema encoding defined by the protocol.
type WellKnownSchemaEncoding uint8

const (
	// SchemaEncodingOther is a well-known encoding not known to this SDK version.
	SchemaEncodingOther WellKnownSchemaEncoding = iota
	SchemaEncodingProtobuf
	SchemaEncodingFlatbuffer
	SchemaEncodingROS1Msg
	SchemaEncodingROS2Msg
	SchemaEncodingROS2IDL
	SchemaEncodingOMGIDL
	SchemaEncodingJSONSchema
)

var wellKnownSchemaEncodingNames = [...]string{"other", "protobuf", "flatbuffer", "ros1msg", "ros2msg", "ros2idl", "omgidl", "jsonschema"}

func (WellKnownSchemaEncoding) isSchemaEncoding() {}

func (e WellKnownSchemaEncoding) String() string {
	if int(e) < len(wellKnownSchemaEncodingNames) {
		return wellKnownSchemaEncodingNames[e]
	}
	return fmt.Sprintf("WellKnownSchemaEncoding(%d)", uint8(e))
}

// CustomSchemaEncoding is an application-defined schema encoding identified by name.
type CustomSchemaEncoding string

func (CustomSchemaEncoding) isSchemaEncoding() {}

func (e CustomSchemaEncoding) String() string {
	return string(e)
}

// FrameEncoding is the encoding of frames pushed on a data track: a WellKnownFrameEncoding or a
// CustomFrameEncoding.
type FrameEncoding interface {
	fmt.Stringer
	// isSelfDescribing reports whether frames need no schema; ok is false when this cannot be determined.
	isSelfDescribing() (selfDescribing, ok bool)
	// isDescribedBy reports whether a schema with the given encoding can describe these frames;
	// ok is false when this cannot be determined.
	isDescribedBy(schema SchemaEncoding) (described, ok bool)
}

// WellKnownFrameEncoding is a frame encoding defined by the protocol.
type WellKnownFrameEncoding uint8

const (
	// FrameEncodingOther is a well-known encoding not known to this SDK version.
	FrameEncodingOther WellKnownFrameEncoding = iota
	FrameEncodingROS1
	FrameEncodingCDR
	FrameEncodingProtobuf
	FrameEncodingFlatbuffer
	FrameEncodingCBOR
	FrameEncodingMsgPack
	FrameEncodingJSON
)

var wellKnownFrameEncodingNames = [...]string{"other", "ros1", "cdr", "protobuf", "flatbuffer", "cbor", "msgpack", "json"}

func (e WellKnownFrameEncoding) String() string {
	if int(e) < len(wellKnownFrameEncodingNames) {
		return wellKnownFrameEncodingNames[e]
	}
	return fmt.Sprintf("WellKnownFrameEncoding(%d)", uint8(e))
}

func (e WellKnownFrameEncoding) isSelfDescribing() (selfDescribing, ok bool) {
	switch e {
	case FrameEncodingCBOR, FrameEncodingMsgPack, FrameEncodingJSON:
		return true, true
	case FrameEncodingOther:
		return false, false
	default:
		return false, true
	}
}

func (e WellKnownFrameEncoding) isDescribedBy(schema SchemaEncoding) (described, ok bool) {
	if e == FrameEncodingOther {
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

// CustomFrameEncoding is an application-defined frame encoding identified by name.
type CustomFrameEncoding string

func (e CustomFrameEncoding) String() string {
	return string(e)
}

func (CustomFrameEncoding) isSelfDescribing() (selfDescribing, ok bool) {
	return false, false
}

func (CustomFrameEncoding) isDescribedBy(SchemaEncoding) (described, ok bool) {
	return false, false
}

// validateSchema checks that the given frame and schema encodings are compatible; nil means unset.
func validateSchema(frameEncoding FrameEncoding, schemaEncoding SchemaEncoding) error {
	switch {
	case frameEncoding == nil && schemaEncoding != nil:
		return ErrSchemaMissingFrameEncoding
	case frameEncoding != nil && schemaEncoding == nil:
		if selfDescribing, ok := frameEncoding.isSelfDescribing(); ok && !selfDescribing {
			return ErrSchemaMissingID
		}
	case frameEncoding != nil && schemaEncoding != nil:
		if described, ok := frameEncoding.isDescribedBy(schemaEncoding); ok && !described {
			return ErrSchemaIncompatible
		}
	}
	return nil
}

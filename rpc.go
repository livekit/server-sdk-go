package lksdk

import (
	"fmt"
	"time"

	"github.com/livekit/protocol/livekit"
)

type RpcErrorCode uint32

const (
	RpcApplicationError RpcErrorCode = 1500 + iota
	RpcConnectionTimeout
	RpcResponseTimeout
	RpcRecipientDisconnected
	RpcResponsePayloadTooLarge
	RpcSendFailed
)

const (
	RpcUnsupportedMethod RpcErrorCode = 1400 + iota
	RpcRecipientNotFound
	RpcRequestPayloadTooLarge
	RpcUnsupportedServer
	RpcUnsupportedVersion
)

const (
	MaxMessageBytes = 256
	MaxDataBytes    = 15360 // 15KiB

	// Maximum payload size for RPC requests and responses. If a payload exceeds this size,
	// the RPC call will fail with a RpcRequestPayloadTooLarge(1402) or RpcResponsePayloadTooLarge(1504) error.
	MaxPayloadBytes = 15360 // 15KiB
)

var rpcErrorMessages = map[RpcErrorCode]string{
	RpcApplicationError:        "Application error in method handler",
	RpcConnectionTimeout:       "Connection timeout",
	RpcResponseTimeout:         "Response timeout",
	RpcRecipientDisconnected:   "Recipient disconnected",
	RpcResponsePayloadTooLarge: "Response payload too large",
	RpcSendFailed:              "Failed to send",

	RpcUnsupportedMethod:      "Method not supported at destination",
	RpcRecipientNotFound:      "Recipient not found",
	RpcRequestPayloadTooLarge: "Request payload too large",
	RpcUnsupportedServer:      "RPC not supported by server",
	RpcUnsupportedVersion:     "Unsupported RPC version",
}

// Parameters for initiating an RPC call
type PerformRpcParams struct {
	// The identity of the destination participant
	DestinationIdentity string
	// The name of the method to call
	Method string
	// The method payload
	Payload string
	// Timeout for receiving a response after the initial connection (in milliseconds).
	// If a value less than 8000 ms is provided, it will be automatically clamped to 8000 ms
	// to ensure sufficient time for round-trip latency buffering.
	// Default: 15000 ms.
	ResponseTimeout *time.Duration
}

// Data passed to method handler for incoming RPC invocations
type RpcInvocationData struct {
	// The unique request ID. Will match at both sides of the call, useful for debugging or logging.
	RequestID string
	// The unique participant identity of the caller.
	CallerIdentity string
	// The payload of the request. User-definable format, could be JSON for example.
	Payload string
	// The maximum time the caller will wait for a response.
	ResponseTimeout time.Duration
}

// Specialized error handling for RPC methods.
//
// Instances of this type, when thrown in a method handler, will have their `message`
// serialized and sent across the wire. The sender will receive an equivalent error on the other side.
//
// Built-in types are included but developers may use any string, with a max length of 256 bytes.
type RpcError struct {
	Code    RpcErrorCode
	Message string
	Data    *string
}

// Creates an error object with the given code and message, plus an optional data payload.
//
// If thrown in an RPC method handler, the error will be sent back to the caller.
//
// Error codes 1001-1999 are reserved for built-in errors.
//
// Maximum message length is 256 bytes, and maximum data payload length is 15KiB.
// If a payload exceeds these limits, it will be truncated.
func NewRpcError(code RpcErrorCode, message string, data *string) *RpcError {
	err := &RpcError{Code: code, Message: truncateBytes(message, MaxMessageBytes)}

	if data != nil {
		truncatedData := truncateBytes(*data, MaxDataBytes)
		err.Data = &truncatedData
	}

	return err
}

func fromProto(proto *livekit.RpcError) *RpcError {
	return &RpcError{
		Code:    RpcErrorCode(proto.Code),
		Message: proto.Message,
		Data:    &proto.Data,
	}
}

func (e RpcError) toProto() *livekit.RpcError {
	err := &livekit.RpcError{
		Code:    uint32(e.Code),
		Message: e.Message,
	}

	if e.Data != nil {
		err.Data = *e.Data
	}

	return err
}

func (e *RpcError) Error() string {
	return fmt.Sprintf("RpcError %d: %s", e.Code, e.Message)
}

// Creates an error object with a built-in (or reserved) code and optional data payload.
func rpcErrorFromBuiltInCodes(code RpcErrorCode, data *string) *RpcError {
	return NewRpcError(code, rpcErrorMessages[code], data)
}

type rpcPendingAckHandler struct {
	resolve             func()
	participantIdentity string
}

type rpcPendingResponseHandler struct {
	resolve             func(payload *string, error *RpcError)
	participantIdentity string
}

type RpcHandlerFunc func(data RpcInvocationData) (string, error)

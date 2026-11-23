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

package cloudagents

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// maxErrorBodySize bounds how much of an error response HTTPError keeps.
const maxErrorBodySize = 64 << 10

// BuildError is a build that cloud-agents reports as failed on the build
// stream, in a "BUILD ERROR:" line. Message is cloud-agents' text.
type BuildError struct {
	Message string
}

func (e *BuildError) Error() string {
	return e.Message
}

// HTTPError is a non-200 response from one of cloud-agents' HTTP routes:
// /build, /logs or /push-target.
type HTTPError struct {
	// StatusCode is the HTTP status code, such as 429.
	StatusCode int
	// Status is the HTTP status, such as "429 Too Many Requests".
	Status string
	// Message and Meta are the msg and meta fields of the JSON body that
	// cloud-agents sends with an error. They are empty when the body has none.
	Message string
	Meta    map[string]string
	// Body is the response body, up to 64 KiB.
	Body []byte

	// text is what Error returns, the same text for each route as before
	// HTTPError existed.
	text string
}

func (e *HTTPError) Error() string {
	if e.text != "" {
		return e.text
	}
	return "cloud-agents returned " + e.Status
}

// RefusedError is a call that cloud-agents refuses with success false in its
// response, such as a deploy of an agent that is already building.
type RefusedError struct {
	// Op is what was refused: "deploy agent" or "promote agent".
	Op string
	// Message is cloud-agents' reason.
	Message string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("failed to %s: %s", e.Op, e.Message)
}

// newHTTPError reads resp, a non-200 response, into an HTTPError. decodeErr
// is set when the body isn't cloud-agents' JSON error; the HTTPError then has
// no Message or Meta.
func newHTTPError(resp *http.Response) (e *HTTPError, decodeErr error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	e = &HTTPError{StatusCode: resp.StatusCode, Status: resp.Status, Body: body}
	if err != nil {
		return e, err
	}
	var apiErr APIError
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&apiErr); err != nil {
		return e, err
	}
	e.Message, e.Meta = apiErr.Message, apiErr.Meta
	return e, nil
}

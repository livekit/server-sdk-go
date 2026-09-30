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

package signalling

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

func ToHttpURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "ws") {
		return strings.Replace(rawURL, "ws", "http", 1)
	}
	return rawURL
}

func ToWebsocketURL(rawURL string) string {
	if strings.HasPrefix(rawURL, "http") {
		return strings.Replace(rawURL, "http", "ws", 1)
	}
	return rawURL
}

func NewHTTPHeaderWithToken(token string) http.Header {
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+token)
	return header
}

// buildURL cleans the path of prefix (duplicate slashes and dot segments are resolved),
// appends path, and sets the query to rawQuery. Any query already on prefix is replaced.
// It returns ErrInvalidParameter if prefix has no host.
func buildURL(prefix, path, rawQuery string) (string, error) {
	u, err := url.Parse(prefix)
	if err != nil {
		return "", err
	}
	// url.Parse accepts scheme-less input such as "localhost:7880", reading
	// "localhost" as the scheme. String() then drops the path for such opaque
	// URLs, so reject anything without a host.
	if u.Host == "" {
		return "", fmt.Errorf("%w: server url %q has no host", ErrInvalidParameter, u.Redacted())
	}
	u = u.JoinPath(path)
	u.RawQuery = rawQuery
	return u.String(), nil
}

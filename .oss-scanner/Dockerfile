# Copyright 2026 LiveKit, Inc.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Build image for Anthropic's OSS Scanner. Keep the Go version at or above go.mod's: GOTOOLCHAIN=local won't fetch one.
FROM golang:1.26-bookworm

ENV DEBIAN_FRONTEND=noninteractive \
    GOTOOLCHAIN=local

RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential ca-certificates curl git pkg-config \
      libopus-dev libopusfile-dev libsoxr-dev \
    && rm -rf /var/lib/apt/lists/*

# The room tests need a real server and the API tests need the mock API server.
# livekit-server's go.mod has replace directives, so `go install @version` can't build it.
ARG LIVEKIT_SERVER_REF=v1.13.11
RUN git clone --depth 1 --branch ${LIVEKIT_SERVER_REF} https://github.com/livekit/livekit /opt/livekit \
    && cd /opt/livekit \
    && go build -o /usr/local/bin/livekit-server ./cmd/server \
    && go build -o /usr/local/bin/test-server ./cmd/test-server

WORKDIR /src
COPY . /src

RUN go mod download && go mod verify
RUN go build ./...

# CI=true skips TestForceTLS, which needs a TURN relay.
RUN (livekit-server --dev --bind 127.0.0.1 --node-ip 127.0.0.1 >/tmp/livekit-server.log 2>&1 &) \
    && (test-server --bind 127.0.0.1 >/tmp/test-server.log 2>&1 &) \
    && for i in $(seq 30); do \
         curl -sf -o /dev/null http://127.0.0.1:7880/ \
         && curl -s -o /dev/null http://127.0.0.1:9999/settings/regions && break; \
         sleep 1; \
       done \
    && CI=true LIVEKIT_KEYS='devkey: secret' go test -count=1 -timeout 20m ./... \
    || echo 'WARNING: baseline tests failed'

ENV GOPROXY=off \
    GOSUMDB=off

CMD ["bash"]

# LiveKit Go server SDK threat model

`github.com/livekit/server-sdk-go/v2` is the Go SDK that backend applications
use to talk to a LiveKit server. It has two halves: Twirp API clients (rooms,
egress, ingress, SIP, agent dispatch, connectors) that mint a short-lived access
token for each call, and a WebRTC room client that joins a room as a
participant, publishes and subscribes to media, and exchanges data packets,
byte and text streams, RPC calls, and data tracks with other participants.

## Scope and trust boundaries

Audit the Go on `main`. Prioritise:

- Room client: `room.go`, `engine.go`, `remoteparticipant.go`, `transport.go`,
  and `signalling/` (WebSocket signalling, join and reconnect).
- Data from other participants: `engine.go` `handleDataPacket`, `streams.go`
  (byte and text stream reassembly), `rpc.go` and the RPC handlers in
  `room.go`, `remotedatatracktransport.go`, and `internal/datatrack/`
  (packet parsing and depacketizing).
- Media from other participants: `packet_trailer*.go` (frame-metadata trailers
  and H.264/H.265 SEI parsing), `pkg/samplebuilder/`, `pkg/synchronizer/`,
  `pkg/interceptor/`, and `pkg/media/` (PCM remote tracks).
- End-to-end encryption: `e2ee/`, `encryption.go`, and `datatrackcryptor.go`,
  which decrypt frames and data that other participants send.
- API clients: `auth.go` and `livekitapi.go` (per-call token minting and
  credential selection), and `failover.go` and `regionurlprovider.go` (region
  discovery and retry).
- `pkg/cloudagents/`: the agent deployment client, including the source tarball
  it builds from a local directory (`tar.go`).

Out of scope: `examples/`, `magefile.go`, generated code, and test helpers.
`pkg/oggreader/` and `readersampleprovider.go` parse files the application
supplies; treat those as application input, not network input.

Untrusted input:

- Other participants in the same room. Their data packets, stream headers,
  chunks and trailers, RPC requests and responses, data-track packets, RTP
  payloads, frame trailers, encrypted frames, and participant metadata and
  attributes are untrusted. The server forwards payloads without interpreting
  them.

Trusted: the application, its API key and secret, the tokens it passes in, the
server URL it configures, and the LiveKit server at that URL. The SDK
authenticates to the server it was pointed at and takes its identity model from
it: `handleDataPacket` attributes data packets, including RPC caller identity,
to `DataPacket.ParticipantIdentity`, which the server overwrites with the
sender's identity (empty for hidden participants), and falls back to the
deprecated in-payload field only when that is empty; participant
lists, permissions, and track assignments arrive over signalling; and on
reconnect or API failover the SDK follows the region URLs the server returns. A
malicious server can already impersonate any participant, so treat a hostile
server as out of scope. Malformed signalling that crashes the SDK is still a
robustness bug (Low).

LiveKit server (`livekit/livekit`) is enrolled separately, and the protocol
library (`livekit/protocol`) has its own threat model. Report a server that fails to stamp or enforce sender
identity there. Access-token and webhook verification, including the webhook
receiver (`webhook.Receive`), live in `livekit/protocol`; this SDK only mints
tokens through `protocol/auth` and has no webhook receiver of its own.

## Security properties

- **Sender attribution:** data packets, stream chunks, stream trailers, RPC
  responses, and data-track packets must be attributed only to the
  server-stamped sender identity, not to identity fields inside the payload.
  A participant must not inject chunks or trailers into, or close, a stream it
  did not open, or answer an RPC sent to another participant.
- **Parsing:** stream headers, data-track packets, frame trailers, SEI units,
  RTP and RTCP, and protobuf or `protojson` data messages from participants
  must not panic, read out of bounds, or loop on malformed input.
- **Resource use:** a participant must not make the SDK allocate memory or
  start goroutines without bound, for example through a large declared stream
  `TotalLength`, many concurrent stream IDs, unanswered RPCs, or data-track
  frames that never complete.
- **End-to-end encryption:** a participant without the shared key must not get
  forged or tampered frames or data accepted as decrypted. Decryption failures
  must drop the frame, not crash or leak plaintext.
- **Credentials:** tokens minted per API call must carry only the grants that
  call needs. An explicit API key and secret must take precedence over ambient
  environment credentials. API secrets and tokens must not appear in logs or
  errors, and must only be sent to the configured host or the regions it
  advertises.
- **Agent deployment tarball:** the tarball must contain only regular files
  under the chosen directory that the exclude rules allow.

## Severity guidance

- **Critical:** remote code execution in the SDK process from another
  participant.
- **High:** a remote crash of the embedding process triggered by another
  participant's data, media, or encrypted frames; sender spoofing that bypasses
  the server-stamped identity; E2EE plaintext disclosure or forgery without the
  key; leaking the API secret.
- **Medium:** unbounded memory or goroutine growth from one participant's
  traffic; sending a token to a host the application did not configure and the
  server did not advertise; tokens minted with broader grants than the call
  needs.
- **Low / not a finding:** behaviour that needs a malicious server, the API
  secret, or application configuration; crashes on malformed files the
  application supplies; crashes from malformed server signalling; media quality,
  bandwidth estimation, or reconnect timing without a security impact.

## Testing

The checkout is at `/src`. Go dependencies are cached. `GOPROXY=off` and
`GOSUMDB=off` prevent downloads. `livekit-server` and the mock API server
`test-server`, both built from `livekit/livekit` at the tag in the Dockerfile,
are in `/usr/local/bin`. Start them first: the room tests connect to
`ws://localhost:7880` with the dev keys in `LIVEKIT_KEYS`, and the API and
failover tests use the mock on `127.0.0.1:9999-10002` and skip if it is not
running. `CI=true` skips `TestForceTLS`, which needs a TURN relay.

The room tests need a non-loopback interface: pion does not gather loopback
ICE candidates, so in a container with only `lo` they time out after 5s. The
`internal/`, `e2ee/`, `pkg/`, and `signalling/` packages and the root-package
API tests run without one. `TestUploadSourceFilePermissions` fails as root,
because root can read a mode-000 file.

```sh
cd /src
livekit-server --dev --bind 127.0.0.1 --node-ip 127.0.0.1 >/tmp/livekit-server.log 2>&1 &
test-server --bind 127.0.0.1 >/tmp/test-server.log 2>&1 &
for i in $(seq 30); do curl -sf -o /dev/null http://127.0.0.1:7880/ && break; sleep 1; done
CI=true LIVEKIT_KEYS='devkey: secret' go test -count=1 ./...
go test -race -count=1 ./internal/... ./e2ee/... ./pkg/... ./signalling/...
```

There are no fuzz tests. For parsers, add a `Fuzz` target in the affected
package (`internal/datatrack`, `e2ee`, or the root package for
`packet_trailer*.go`). Use the room tests in `integration_test.go` and
`datatrack_integration_test.go` as templates for reproducers that need a second
participant.

## Reports

State the attacker's required access (another participant in the same room,
with or without a specific grant, or the E2EE key), the entry point, and the
demonstrated impact. Provide a minimal reproducer, preferably a Go test in the
affected package.

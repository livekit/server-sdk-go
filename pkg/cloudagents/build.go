// Copyright 2025 LiveKit, Inc.
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
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/progress/progressui"
	"golang.org/x/sync/errgroup"
)

// queueEvent is a build-queue status update the server sends to v2 clients, as
// {"lkQueue": {...}}. A buildkit SolveStatus never carries an lkQueue field, so this
// shape is unambiguous. It has no vertex, so it does not start the build clock.
type queueEvent struct {
	Message string `json:"message"`
}

type queueEnvelope struct {
	LkQueue *queueEvent `json:"lkQueue"`
}

func (c *Client) build(ctx context.Context, id string, attributes map[string]string, agentDeployment string, writer io.Writer) error {
	params := url.Values{}
	params.Add("agent_id", id)
	if agentDeployment != "" {
		params.Add("deployment", agentDeployment)
	}

	// Attributes travel in the X-LIVEKIT-AGENT-VERSION-ATTRIBUTES header (set by
	// newRequestWithContext), the same channel BYOC pushes use. cloud-agents still
	// accepts the legacy `attributes` query param from older CLI versions.
	fullUrl := fmt.Sprintf("%s/build?%s", c.agentsURL, params.Encode())
	req, err := c.newRequestWithContext(ctx, "POST", fullUrl, nil, attributes)
	if err != nil {
		return err
	}
	// Tell the server we can render queue events ourselves (lkQueue lines), so the queue
	// wait is shown separately and does not count toward the build clock. Old servers
	// ignore the header and send the queue wait as buildkit vertices, which still work.
	req.Header.Set("X-LIVEKIT-BUILD-PROTOCOL", "v2")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to build agent: %s", resp.Status)
	}

	displayMode := progressui.AutoMode
	if c.jsonLogStream {
		displayMode = progressui.RawJSONMode
	}

	ch := make(chan *bkclient.SolveStatus)
	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		// Defer creating the progress display until the first build status arrives. The
		// queue-phase lines (detach notice, "Waiting for an available builder") are printed
		// directly to writer; starting the renderer earlier emits a stray blank line among
		// them and would anchor the build clock on the queue wait.
		first, ok := <-ch
		if !ok {
			return nil // stream ended before any build status (e.g. a queue-phase failure)
		}
		display, err := progressui.NewDisplay(writer, displayMode)
		if err != nil {
			return err
		}
		forward := make(chan *bkclient.SolveStatus)
		go func() {
			defer close(forward)
			// Guard every send with ctx.Done() so the relay can't block forever if the
			// display stops reading (e.g. UpdateFrom returned an error): the errgroup
			// cancels ctx on that error, which unblocks and drains this goroutine.
			send := func(s *bkclient.SolveStatus) bool {
				select {
				case forward <- s:
					return true
				case <-ctx.Done():
					return false
				}
			}
			if !send(first) {
				return
			}
			for s := range ch {
				if !send(s) {
					return
				}
			}
		}()
		_, err = display.UpdateFrom(context.Background(), forward)
		return err
	})

	eg.Go(func() error {
		defer close(ch)
		var lastQueue string
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, bufio.MaxScanTokenSize), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if bytes.HasPrefix(line, []byte("BUILD ERROR:")) {
				return errors.New(strings.TrimPrefix(scanner.Text(), "BUILD ERROR: "))
			}

			// A queue event carries an lkQueue field; a build update does not. It bypasses the
			// progress display so it does not start the build clock, and is de-duped so a
			// repeated heartbeat is emitted once. In JSON-log mode the stream must stay valid
			// JSON for machine consumers, so pass the raw lkQueue line through (it decodes to
			// an empty SolveStatus, harmless) instead of writing the human-readable text.
			var env queueEnvelope
			if json.Unmarshal(line, &env) == nil && env.LkQueue != nil {
				if msg := env.LkQueue.Message; msg != "" && msg != lastQueue {
					lastQueue = msg
					if c.jsonLogStream {
						_, _ = writer.Write(line)
						_, _ = io.WriteString(writer, "\n")
					} else {
						fmt.Fprintln(writer, msg)
					}
				}
				continue
			}

			var status bkclient.SolveStatus
			if err := json.Unmarshal(line, &status); err != nil {
				return fmt.Errorf("decode error: %w", err)
			}
			select {
			case ch <- &status:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return scanner.Err()
	})

	if err := eg.Wait(); err != nil {
		return err
	}

	return nil
}

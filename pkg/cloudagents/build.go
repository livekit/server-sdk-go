package cloudagents

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/livekit/protocol/auth"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/util/progress/progressui"
	"golang.org/x/sync/errgroup"
)

func (c *Client) build(ctx context.Context, id string) error {
	params := url.Values{}
	params.Add("agent_id", id)
	fullUrl := fmt.Sprintf("%s/build?%s", c.agentsURL, params.Encode())
	req, err := c.newRequest("POST", fullUrl, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to build agent: %s", resp.Status)
	}

	ch := make(chan *bkclient.SolveStatus)
	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		display, err := progressui.NewDisplay(os.Stderr, "auto")
		if err != nil {
			return err
		}
		_, err = display.UpdateFrom(context.Background(), ch)
		return err
	})

	eg.Go(func() error {
		defer close(ch)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "BUILD ERROR:") {
				return errors.New(strings.TrimPrefix(line, "BUILD ERROR: "))
			}

			var status bkclient.SolveStatus
			if err := json.Unmarshal(scanner.Bytes(), &status); err != nil {
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

func (c *Client) newRequest(method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if err := c.setAuthToken(req); err != nil {
		return nil, err
	}
	setLivekitVersionHeader(req)
	return req, nil
}

func (c *Client) setAuthToken(req *http.Request) error {
	at := auth.NewAccessToken(c.apiKey, c.apiSecret)
	at.SetAgentGrant(&auth.AgentGrant{Admin: true})
	token, err := at.ToJWT()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	return nil
}

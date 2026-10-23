// Package agent implements the telegram-bot-api deployment agent: it
// registers itself with KubeForecast, heartbeats, pulls its desired container
// state, and reconciles a single local Docker container to match (see
// docker.go). It deliberately manages only one container — this is meant to
// stand up one self-hosted Bot API instance for KubeForecast's own outbound
// notifications, not to orchestrate a fleet.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/cylonchau/kubeforecast/pkg/apis"
	"io"
	"net/http"
	"time"
)

// Client talks to the KubeForecast control plane's bot-api-nodes agent API.
type Client struct {
	serverURL  string
	token      string
	httpClient *http.Client
}

func NewClient(serverURL, token string) *Client {
	return &Client{serverURL: serverURL, token: token, httpClient: &http.Client{Timeout: 15 * time.Second}}
}

// SetToken updates the bearer token used for subsequent authenticated calls
// (heartbeat, config snapshot) once Register hands one back.
func (c *Client) SetToken(token string) { c.token = token }

type apiResponse struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// Register performs one register/poll call. nodeID is 0 on first contact.
func (c *Client) Register(ctx context.Context, req apis.TelegramBotAPINodeRegisterReq) (*apis.TelegramBotAPINodeSession, error) {
	var session apis.TelegramBotAPINodeSession
	if err := c.postJSON(ctx, "/api/v1/bot-api-nodes/register", req, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

// Heartbeat reports observed container state; nodeID identifies the node.
func (c *Client) Heartbeat(ctx context.Context, nodeID int64, req apis.TelegramBotAPINodeHeartbeatReq) error {
	return c.postJSON(ctx, fmt.Sprintf("/api/v1/bot-api-nodes/%d/heartbeat", nodeID), req, nil)
}

// FetchConfigSnapshot pulls the node's current desired state.
func (c *Client) FetchConfigSnapshot(ctx context.Context, nodeID int64) (*apis.TelegramBotAPINodeConfigSnapshot, error) {
	url := fmt.Sprintf("%s/api/v1/bot-api-nodes/%d/config/snapshot", c.serverURL, nodeID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	c.authorize(httpReq)
	res, err := c.do(httpReq)
	if err != nil {
		return nil, err
	}
	var snapshot apis.TelegramBotAPINodeConfigSnapshot
	if err := json.Unmarshal(res.Data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode config snapshot: %w", err)
	}
	return &snapshot, nil
}

func (c *Client) postJSON(ctx context.Context, path string, payload, out any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.serverURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.authorize(httpReq)
	res, err := c.do(httpReq)
	if err != nil {
		return err
	}
	if out != nil && len(res.Data) > 0 {
		return json.Unmarshal(res.Data, out)
	}
	return nil
}

func (c *Client) authorize(req *http.Request) {
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

func (c *Client) do(httpReq *http.Request) (*apiResponse, error) {
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var res apiResponse
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, fmt.Errorf("invalid server response (http %d): %w", resp.StatusCode, err)
	}
	if res.Code != 10000 {
		return nil, fmt.Errorf("server error (%d): %s", res.Code, res.Msg)
	}
	return &res, nil
}

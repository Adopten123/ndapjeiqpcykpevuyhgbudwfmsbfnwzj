package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"rzdarctica.local/backend/internal/features/calculations"
)

type ComputeClient struct {
	base   string
	client *http.Client
}

func NewComputeClient(base string) *ComputeClient {
	return &ComputeClient{base: strings.TrimRight(base, "/"), client: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

type ComputeHTTPError struct{ Status int }

func (e *ComputeHTTPError) Error() string { return fmt.Sprintf("compute response status %d", e.Status) }

func (c *ComputeClient) request(ctx context.Context, method, path string, body any, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return calculations.ErrCompute
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return calculations.ErrCompute
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.client.Do(req)
	if err != nil {
		return calculations.ErrCompute
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return &ComputeHTTPError{Status: res.StatusCode}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 2*1024*1024+1))
	if err != nil || len(data) > 2*1024*1024 || json.Unmarshal(data, result) != nil {
		return calculations.ErrCompute
	}
	return nil
}

func (c *ComputeClient) Health(ctx context.Context) error {
	var result struct {
		Status string `json:"status"`
	}
	if err := c.request(ctx, http.MethodGet, "/health", nil, &result); err != nil {
		return err
	}
	if result.Status != "ok" {
		return calculations.ErrCompute
	}
	return nil
}

func (c *ComputeClient) Scenarios(ctx context.Context) ([]calculations.Scenario, error) {
	var result []calculations.Scenario
	err := c.request(ctx, http.MethodGet, "/v1/demo/scenarios", nil, &result)
	if err == nil && len(result) != 4 {
		err = calculations.ErrCompute
	}
	return result, err
}

func (c *ComputeClient) Snapshot(ctx context.Context, id string) (calculations.Snapshot, error) {
	var result calculations.Snapshot
	err := c.request(ctx, http.MethodGet, "/v1/demo/scenarios/"+id, nil, &result)
	if err == nil && (result.ScenarioID != id || result.EngineProfile != calculations.Profile || len(result.InputSHA256) != 64 || result.SnapshotID == "" || len(result.Payload) == 0) {
		err = calculations.ErrCompute
	}
	return result, err
}

func (c *ComputeClient) Solve(ctx context.Context, request calculations.ComputeRequest) (calculations.ComputeResponse, error) {
	var result calculations.ComputeResponse
	err := c.request(ctx, http.MethodPost, "/v1/research/solve", request, &result)
	return result, err
}

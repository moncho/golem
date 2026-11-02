package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Remote runs commands on an execution endpoint served by Handler.
type Remote struct {
	url, token string
	client     *http.Client
}

func NewRemote(url, token string) *Remote {
	return &Remote{url: strings.TrimRight(url, "/"), token: token, client: &http.Client{Timeout: 10 * time.Second}}
}

func (r *Remote) call(ctx context.Context, method, path string, body, result any) error {
	var reader *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	} else {
		reader = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.url+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+r.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 500:
		return ErrUnavailable
	case resp.StatusCode == http.StatusGone:
		return ErrOutputExpired
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return errors.New("execution request rejected")
	}
	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return errors.New("invalid execution response")
	}
	return nil
}

func (r *Remote) Start(ctx context.Context, s Spec) (Job, error) {
	var j Job
	err := r.call(ctx, "PUT", "/jobs/"+s.ID, s, &j)
	return j, err
}

func (r *Remote) Poll(ctx context.Context, id string) (Job, error) {
	var j Job
	err := r.call(ctx, "GET", "/jobs/"+id, nil, &j)
	return j, err
}

func (r *Remote) Stop(ctx context.Context, id string) error {
	var out map[string]bool
	return r.call(ctx, "POST", "/jobs/"+id+"/stop", nil, &out)
}

func (r *Remote) Output(ctx context.Context, id string, offset int64, limit int) (Chunk, error) {
	var c Chunk
	err := r.call(ctx, "GET", fmt.Sprintf("/jobs/%s/output?offset=%d&limit=%d", id, offset, limit), nil, &c)
	return c, err
}

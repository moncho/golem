package console

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

const (
	clientDeadline = 15 * time.Second
	maxResponse    = 4_000_000
)

// newKey returns a random UUID (version 4) for use as a request key.
func newKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Call sends one request to the controller and returns its result. An empty
// Key is filled with a fresh one. A rejected answer is returned as *host.Error.
func Call(state string, req Request) (json.RawMessage, error) {
	if req.Key == "" {
		req.Key = newKey()
	}
	c, err := net.DialTimeout("unix", SocketPath(state), clientDeadline)
	if err != nil {
		return nil, errors.New("Controller unavailable. Start golem serve with the same --state directory.")
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(clientDeadline))
	data, err := json.Marshal(req)
	if err != nil {
		return nil, errors.New("Controller request failed.")
	}
	if _, err := c.Write(append(data, '\n')); err != nil {
		return nil, callError(err)
	}
	line, err := bufio.NewReader(io.LimitReader(c, maxResponse+1)).ReadBytes('\n')
	if len(line) > maxResponse {
		return nil, errors.New("Controller response exceeded the limit.")
	}
	if err != nil {
		return nil, callError(err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, errors.New("Invalid controller response.")
	}
	if resp.OK {
		return resp.Result, nil
	}
	if resp.Error == nil {
		return nil, errors.New("Controller request failed.")
	}
	if resp.Error.Code == "rejected" {
		return nil, resp.Error
	}
	return nil, errors.New(resp.Error.Message)
}

func callError(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return errors.New("Controller request timed out. Delivery may be uncertain; retry with the same request key.")
	}
	return errors.New("Controller disconnected before acknowledgement.")
}

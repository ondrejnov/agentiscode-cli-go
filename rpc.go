package agentiscode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const AuthHeader = "X-Auth-Token"
const ServiceAuthHeader = "X-Service-Token"

var serviceMethods = map[string]bool{
	"task.add_agent_comment": true, "task.upload_artifact": true, "task.add_question": true, "task.get_question_result": true,
	"task.add_approve": true, "task.get_approve_result": true, "session.session_created": true, "session.session_update": true,
	"session.store_activity_log": true, "session.session_error": true,
}

type RPCError struct {
	Message    string
	StatusCode int
	Details    any
}

func (e *RPCError) Error() string { return e.Message }

type RPC interface {
	Call(context.Context, string, any) (any, error)
}

type Client struct {
	Endpoint, Token, ServiceToken string
	HTTP                          *http.Client
	transport                     *http.Transport
}

func NormalizeEndpoint(endpoint string) (string, error) {
	s := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if s == "" {
		return "", fmt.Errorf("endpoint must not be empty")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("endpoint must be an absolute HTTP(S) URL")
	}
	if !strings.HasSuffix(s, "/api") {
		s += "/api"
	}
	return s, nil
}

func NewClient(endpoint, token, serviceToken string, timeout time.Duration) (*Client, error) {
	s, err := NormalizeEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// Never forward either credential to an unexpected redirect destination.
	h := &http.Client{Timeout: timeout, Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{Endpoint: s, Token: token, ServiceToken: serviceToken, HTTP: h, transport: tr}, nil
}
func (c *Client) Close() {
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
}
func (c *Client) Call(ctx context.Context, method string, params any) (any, error) {
	return c.CallWithID(ctx, method, params, newID("agentiscode-"+method+"-"))
}
func (c *Client) CallWithID(ctx context.Context, method string, params, id any) (any, error) {
	if id == nil {
		id = newID("agentis-")
	}
	payload := Object{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		payload["params"] = params
	}
	b, err := encodeJSON(payload)
	if err != nil {
		return nil, &RPCError{Message: "Agentis JSON-RPC encoding failed: " + err.Error()}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(b))
	if err != nil {
		return nil, &RPCError{Message: "Agentis JSON-RPC request failed"}
	}
	req.Header.Set("Content-Type", "application/json")
	if serviceMethods[method] {
		if c.ServiceToken != "" {
			req.Header.Set(ServiceAuthHeader, c.ServiceToken)
		}
	} else if c.Token != "" {
		req.Header.Set(AuthHeader, c.Token)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, &RPCError{Message: "Agentis JSON-RPC request failed: " + err.Error()}
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, &RPCError{Message: "Agentis response read failed: " + err.Error(), StatusCode: res.StatusCode}
	}
	var body any
	if json.Unmarshal(data, &body) != nil {
		body = string(data)
	}
	if res.StatusCode >= 300 {
		return nil, &RPCError{Message: fmt.Sprintf("Agentis returned HTTP %d", res.StatusCode), StatusCode: res.StatusCode, Details: body}
	}
	m := obj(body)
	if m == nil {
		return nil, &RPCError{Message: "Agentis returned an invalid JSON-RPC response", StatusCode: res.StatusCode, Details: body}
	}
	if e := m["error"]; e != nil {
		message := str(first(obj(e)["message"], "Agentis returned a JSON-RPC error"))
		return nil, &RPCError{Message: message, StatusCode: res.StatusCode, Details: e}
	}
	return m["result"], nil
}

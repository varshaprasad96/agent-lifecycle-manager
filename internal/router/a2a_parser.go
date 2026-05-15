/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package router

import "encoding/json"

const (
	HeaderA2AMethod    = "x-a2a-method"
	HeaderA2ASessionID = "x-a2a-session-id"
	HeaderA2ATaskID    = "x-a2a-task-id"
)

// A2ARequest represents a parsed A2A JSON-RPC request.
type A2ARequest struct {
	Method    string
	SessionID string
	TaskID    string
}

// ParseA2A extracts A2A protocol fields from a JSON-RPC body.
func ParseA2A(body []byte) (*A2ARequest, error) {
	var rpc struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return nil, err
	}

	req := &A2ARequest{Method: rpc.Method}

	if len(rpc.Params) > 0 {
		var params map[string]interface{}
		if err := json.Unmarshal(rpc.Params, &params); err == nil {
			if sid, ok := params["sessionId"].(string); ok {
				req.SessionID = sid
			}
			if tid, ok := params["taskId"].(string); ok {
				req.TaskID = tid
			}
		}
	}

	return req, nil
}

// Headers returns the routing headers to set on the request.
func (r *A2ARequest) Headers() map[string]string {
	h := map[string]string{
		HeaderA2AMethod: r.Method,
	}
	if r.SessionID != "" {
		h[HeaderA2ASessionID] = r.SessionID
	}
	if r.TaskID != "" {
		h[HeaderA2ATaskID] = r.TaskID
	}
	return h
}

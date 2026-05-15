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
	HeaderMCPMethod   = "x-mcp-method"
	HeaderMCPToolName = "x-mcp-toolname"
)

// MCPRequest represents a parsed MCP JSON-RPC request.
type MCPRequest struct {
	Method   string
	ToolName string
}

// ParseMCP extracts MCP protocol fields from a JSON-RPC body.
func ParseMCP(body []byte) (*MCPRequest, error) {
	var rpc struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &rpc); err != nil {
		return nil, err
	}

	req := &MCPRequest{Method: rpc.Method}

	if rpc.Method == "tools/call" && len(rpc.Params) > 0 {
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(rpc.Params, &params); err == nil {
			req.ToolName = params.Name
		}
	}

	return req, nil
}

// Headers returns the routing headers to set on the request.
func (r *MCPRequest) Headers() map[string]string {
	h := map[string]string{
		HeaderMCPMethod: r.Method,
	}
	if r.ToolName != "" {
		h[HeaderMCPToolName] = r.ToolName
	}
	return h
}

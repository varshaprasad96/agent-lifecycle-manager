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

import (
	"io"
	"log/slog"
	"strings"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocv3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements the Envoy ExternalProcessor gRPC service.
type Server struct {
	extprocv3.UnimplementedExternalProcessorServer
	sessions *SessionStore
	logger   *slog.Logger
}

// NewServer creates a new ext_proc server.
func NewServer(sessions *SessionStore, logger *slog.Logger) *Server {
	return &Server{
		sessions: sessions,
		logger:   logger,
	}
}

// Process handles the bidirectional stream from Envoy.
func (s *Server) Process(stream extprocv3.ExternalProcessor_ProcessServer) error {
	var requestBody []byte

	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return status.Errorf(codes.Internal, "recv error: %v", err)
		}

		var resp *extprocv3.ProcessingResponse

		switch v := req.Request.(type) {
		case *extprocv3.ProcessingRequest_RequestHeaders:
			resp = s.processRequestHeaders(v.RequestHeaders)

		case *extprocv3.ProcessingRequest_RequestBody:
			requestBody = v.RequestBody.GetBody()
			resp = s.processRequestBody(requestBody)

		case *extprocv3.ProcessingRequest_ResponseHeaders:
			resp = &extprocv3.ProcessingResponse{
				Response: &extprocv3.ProcessingResponse_ResponseHeaders{
					ResponseHeaders: &extprocv3.HeadersResponse{},
				},
			}

		case *extprocv3.ProcessingRequest_ResponseBody:
			resp = &extprocv3.ProcessingResponse{
				Response: &extprocv3.ProcessingResponse_ResponseBody{
					ResponseBody: &extprocv3.BodyResponse{},
				},
			}

		default:
			resp = &extprocv3.ProcessingResponse{}
		}

		if err := stream.Send(resp); err != nil {
			return status.Errorf(codes.Internal, "send error: %v", err)
		}
	}
}

func (s *Server) processRequestHeaders(headers *extprocv3.HttpHeaders) *extprocv3.ProcessingResponse {
	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &extprocv3.HeadersResponse{},
		},
	}
}

func (s *Server) processRequestBody(body []byte) *extprocv3.ProcessingResponse {
	headers := s.parseProtocol(body)

	if len(headers) == 0 {
		return &extprocv3.ProcessingResponse{
			Response: &extprocv3.ProcessingResponse_RequestBody{
				RequestBody: &extprocv3.BodyResponse{},
			},
		}
	}

	headerMutations := make([]*corev3.HeaderValueOption, 0, len(headers))
	for k, v := range headers {
		headerMutations = append(headerMutations, &corev3.HeaderValueOption{
			Header: &corev3.HeaderValue{
				Key:      k,
				RawValue: []byte(v),
			},
			AppendAction: corev3.HeaderValueOption_OVERWRITE_IF_EXISTS_OR_ADD,
		})
	}

	return &extprocv3.ProcessingResponse{
		Response: &extprocv3.ProcessingResponse_RequestBody{
			RequestBody: &extprocv3.BodyResponse{
				Response: &extprocv3.CommonResponse{
					HeaderMutation: &extprocv3.HeaderMutation{
						SetHeaders: headerMutations,
					},
				},
			},
		},
	}
}

func (s *Server) parseProtocol(body []byte) map[string]string {
	if len(body) == 0 {
		return nil
	}

	if a2a, err := ParseA2A(body); err == nil && isA2AMethod(a2a.Method) {
		s.logger.Info("parsed A2A request", "method", a2a.Method, "sessionId", a2a.SessionID)
		return a2a.Headers()
	}

	if mcp, err := ParseMCP(body); err == nil && isMCPMethod(mcp.Method) {
		s.logger.Info("parsed MCP request", "method", mcp.Method, "toolName", mcp.ToolName)
		return mcp.Headers()
	}

	return nil
}

func isA2AMethod(method string) bool {
	return strings.HasPrefix(method, "message/") ||
		strings.HasPrefix(method, "tasks/") ||
		method == "agent/authenticatedExtendedCard"
}

func isMCPMethod(method string) bool {
	return strings.HasPrefix(method, "tools/") ||
		method == "initialize" ||
		method == "initialized" ||
		strings.HasPrefix(method, "resources/") ||
		strings.HasPrefix(method, "prompts/")
}

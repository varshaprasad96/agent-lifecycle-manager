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
	"testing"
	"time"
)

func TestParseA2A_MessageSend(t *testing.T) {
	body := []byte(`{
		"jsonrpc": "2.0",
		"method": "message/send",
		"params": {
			"sessionId": "sess-123",
			"taskId": "task-456",
			"message": {"role": "user", "parts": [{"text": "hello"}]}
		}
	}`)

	req, err := ParseA2A(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Method != "message/send" {
		t.Errorf("method = %q, want %q", req.Method, "message/send")
	}
	if req.SessionID != "sess-123" {
		t.Errorf("sessionId = %q, want %q", req.SessionID, "sess-123")
	}
	if req.TaskID != "task-456" {
		t.Errorf("taskId = %q, want %q", req.TaskID, "task-456")
	}

	headers := req.Headers()
	if headers[HeaderA2AMethod] != "message/send" {
		t.Errorf("header %s = %q, want %q", HeaderA2AMethod, headers[HeaderA2AMethod], "message/send")
	}
	if headers[HeaderA2ASessionID] != "sess-123" {
		t.Errorf("header %s = %q, want %q", HeaderA2ASessionID, headers[HeaderA2ASessionID], "sess-123")
	}
	if headers[HeaderA2ATaskID] != "task-456" {
		t.Errorf("header %s = %q, want %q", HeaderA2ATaskID, headers[HeaderA2ATaskID], "task-456")
	}
}

func TestParseA2A_NoParams(t *testing.T) {
	body := []byte(`{"jsonrpc": "2.0", "method": "tasks/get"}`)

	req, err := ParseA2A(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Method != "tasks/get" {
		t.Errorf("method = %q, want %q", req.Method, "tasks/get")
	}
	if req.SessionID != "" {
		t.Errorf("sessionId should be empty, got %q", req.SessionID)
	}
}

func TestParseA2A_InvalidJSON(t *testing.T) {
	_, err := ParseA2A([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseMCP_ToolsCall(t *testing.T) {
	body := []byte(`{
		"jsonrpc": "2.0",
		"method": "tools/call",
		"params": {
			"name": "get_temperature",
			"arguments": {"city": "NYC"}
		}
	}`)

	req, err := ParseMCP(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Method != "tools/call" {
		t.Errorf("method = %q, want %q", req.Method, "tools/call")
	}
	if req.ToolName != "get_temperature" {
		t.Errorf("toolName = %q, want %q", req.ToolName, "get_temperature")
	}

	headers := req.Headers()
	if headers[HeaderMCPMethod] != "tools/call" {
		t.Errorf("header %s = %q, want %q", HeaderMCPMethod, headers[HeaderMCPMethod], "tools/call")
	}
	if headers[HeaderMCPToolName] != "get_temperature" {
		t.Errorf("header %s = %q, want %q", HeaderMCPToolName, headers[HeaderMCPToolName], "get_temperature")
	}
}

func TestParseMCP_ToolsList(t *testing.T) {
	body := []byte(`{"jsonrpc": "2.0", "method": "tools/list"}`)

	req, err := ParseMCP(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Method != "tools/list" {
		t.Errorf("method = %q, want %q", req.Method, "tools/list")
	}
	if req.ToolName != "" {
		t.Errorf("toolName should be empty for tools/list, got %q", req.ToolName)
	}
}

func TestParseMCP_Initialize(t *testing.T) {
	body := []byte(`{"jsonrpc": "2.0", "method": "initialize", "params": {}}`)

	req, err := ParseMCP(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Method != "initialize" {
		t.Errorf("method = %q, want %q", req.Method, "initialize")
	}
}

func TestSessionStore_CreateAndGet(t *testing.T) {
	store := NewSessionStore(time.Hour)

	id := store.Create()
	if id == "" {
		t.Fatal("session ID should not be empty")
	}

	sess := store.Get(id)
	if sess == nil {
		t.Fatal("session should exist")
	}
	if sess.ID != id {
		t.Errorf("session ID = %q, want %q", sess.ID, id)
	}

	if store.Len() != 1 {
		t.Errorf("store length = %d, want 1", store.Len())
	}
}

func TestSessionStore_Expiry(t *testing.T) {
	store := NewSessionStore(time.Millisecond)

	id := store.Create()
	time.Sleep(5 * time.Millisecond)

	sess := store.Get(id)
	if sess != nil {
		t.Error("expired session should return nil")
	}
}

func TestSessionStore_BackendSession(t *testing.T) {
	store := NewSessionStore(time.Hour)

	id := store.Create()
	store.SetBackendSession(id, "weather-agent", "backend-sess-123")

	sess := store.Get(id)
	if sess.Backend["weather-agent"] != "backend-sess-123" {
		t.Errorf("backend session = %q, want %q", sess.Backend["weather-agent"], "backend-sess-123")
	}
}

func TestSessionStore_Cleanup(t *testing.T) {
	store := NewSessionStore(time.Millisecond)

	store.Create()
	store.Create()
	time.Sleep(5 * time.Millisecond)

	removed := store.Cleanup()
	if removed != 2 {
		t.Errorf("removed = %d, want 2", removed)
	}
	if store.Len() != 0 {
		t.Errorf("store length = %d, want 0", store.Len())
	}
}

func TestSessionStore_GetNonExistent(t *testing.T) {
	store := NewSessionStore(time.Hour)
	if store.Get("nonexistent") != nil {
		t.Error("should return nil for nonexistent session")
	}
}

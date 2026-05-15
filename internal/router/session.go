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
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const defaultSessionTTL = 24 * time.Hour

// Session maps a gateway session to per-agent backend sessions.
type Session struct {
	ID        string
	CreatedAt time.Time
	ExpiresAt time.Time
	Backend   map[string]string // agent name → backend session ID
}

// SessionStore manages in-memory gateway sessions.
type SessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	ttl      time.Duration
}

// NewSessionStore creates a session store with the given TTL.
func NewSessionStore(ttl time.Duration) *SessionStore {
	if ttl <= 0 {
		ttl = defaultSessionTTL
	}
	return &SessionStore{
		sessions: make(map[string]*Session),
		ttl:      ttl,
	}
}

// Create creates a new gateway session and returns its ID.
func (s *SessionStore) Create() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := generateSessionID()
	now := time.Now()
	s.sessions[id] = &Session{
		ID:        id,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
		Backend:   make(map[string]string),
	}
	return id
}

// Get returns a session by ID, or nil if not found or expired.
func (s *SessionStore) Get(id string) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.sessions[id]
	if !ok {
		return nil
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil
	}
	return sess
}

// SetBackendSession maps a gateway session to a backend agent session.
func (s *SessionStore) SetBackendSession(gatewaySessionID, agentName, backendSessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[gatewaySessionID]
	if !ok {
		return
	}
	sess.Backend[agentName] = backendSessionID
}

// Cleanup removes expired sessions.
func (s *SessionStore) Cleanup() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	removed := 0
	for id, sess := range s.sessions {
		if now.After(sess.ExpiresAt) {
			delete(s.sessions, id)
			removed++
		}
	}
	return removed
}

// Len returns the number of active sessions.
func (s *SessionStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

func generateSessionID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

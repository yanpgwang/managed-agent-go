package sandbox

import (
	"context"
	"sync"
)

type memoryBindingStore struct {
	mu       sync.Mutex
	bindings map[string]Binding
}

func newMemoryBindingStore() *memoryBindingStore {
	return &memoryBindingStore{bindings: make(map[string]Binding)}
}

func (s *memoryBindingStore) GetSandboxBinding(
	_ context.Context,
	sessionID string,
) (Binding, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.bindings[sessionID]
	return binding, ok, nil
}

func (s *memoryBindingStore) PutSandboxBinding(
	_ context.Context,
	binding Binding,
) (Binding, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.bindings[binding.SessionID]; ok {
		return current, nil
	}
	s.bindings[binding.SessionID] = binding
	return binding, nil
}

func (s *memoryBindingStore) DeleteSandboxBinding(
	_ context.Context,
	binding Binding,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.bindings[binding.SessionID]; ok && current.Ref == binding.Ref {
		delete(s.bindings, binding.SessionID)
	}
	return nil
}

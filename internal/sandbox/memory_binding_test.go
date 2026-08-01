package sandbox

import (
	"context"
	"sync"
)

type memoryBindingStore struct {
	mu       sync.Mutex
	bindings map[string]Binding
	intents  map[string]ProvisioningIntent
}

func newMemoryBindingStore() *memoryBindingStore {
	return &memoryBindingStore{
		bindings: make(map[string]Binding),
		intents:  make(map[string]ProvisioningIntent),
	}
}

func (s *memoryBindingStore) GetSandboxProvisioningIntent(
	_ context.Context,
	sessionID string,
) (ProvisioningIntent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent, ok := s.intents[sessionID]
	return intent, ok, nil
}

func (s *memoryBindingStore) PutSandboxProvisioningIntent(
	_ context.Context,
	intent ProvisioningIntent,
) (ProvisioningIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.intents[intent.SessionID]; ok {
		return current, nil
	}
	s.intents[intent.SessionID] = intent
	return intent, nil
}

func (s *memoryBindingStore) ListSandboxProvisioningIntents(
	_ context.Context,
	provider string,
	limit int,
) ([]ProvisioningIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	intents := make([]ProvisioningIntent, 0, len(s.intents))
	for _, intent := range s.intents {
		if intent.Provider == provider && len(intents) < limit {
			intents = append(intents, intent)
		}
	}
	return intents, nil
}

func (s *memoryBindingStore) DeleteSandboxProvisioningIntent(
	_ context.Context,
	intent ProvisioningIntent,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.intents[intent.SessionID]; ok &&
		current.Provider == intent.Provider && current.SpecHash == intent.SpecHash {
		delete(s.intents, intent.SessionID)
	}
	return nil
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
		delete(s.intents, binding.SessionID)
		return current, nil
	}
	s.bindings[binding.SessionID] = binding
	delete(s.intents, binding.SessionID)
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

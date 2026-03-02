package state

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Store manages state persistence
type Store struct {
	basePath string // ~/.openctl/state
}

// NewStore creates a new state store
func NewStore() (*Store, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	basePath := filepath.Join(homeDir, ".openctl", "state")
	if err := os.MkdirAll(basePath, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	return &Store{basePath: basePath}, nil
}

// NewStoreWithPath creates a new state store with a custom base path
func NewStoreWithPath(basePath string) (*Store, error) {
	if err := os.MkdirAll(basePath, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}
	return &Store{basePath: basePath}, nil
}

// Get retrieves state for a resource
func (s *Store) Get(provider, name string) (*State, error) {
	path := s.statePath(provider, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("state not found: %s/%s", provider, name)
		}
		return nil, fmt.Errorf("failed to read state: %w", err)
	}

	var state State
	if err := yaml.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to parse state: %w", err)
	}

	return &state, nil
}

// List returns all states for a provider
func (s *Store) List(provider string) ([]*State, error) {
	providerPath := filepath.Join(s.basePath, provider)

	// If provider directory doesn't exist, return empty list
	if _, err := os.Stat(providerPath); os.IsNotExist(err) {
		return nil, nil
	}

	entries, err := os.ReadDir(providerPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read state directory: %w", err)
	}

	states := make([]*State, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}

		name := entry.Name()[:len(entry.Name())-5] // Remove .yaml extension
		state, err := s.Get(provider, name)
		if err != nil {
			continue // Skip invalid state files
		}
		states = append(states, state)
	}

	return states, nil
}

// ListAll returns all states across all providers
func (s *Store) ListAll() ([]*State, error) {
	entries, err := os.ReadDir(s.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read state directory: %w", err)
	}

	var allStates []*State
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		provider := entry.Name()
		states, err := s.List(provider)
		if err != nil {
			continue
		}
		allStates = append(allStates, states...)
	}

	return allStates, nil
}

// Save persists state to disk
func (s *Store) Save(state *State) error {
	if state.Metadata.Name == "" {
		return fmt.Errorf("state name is required")
	}
	if state.Metadata.Provider == "" {
		return fmt.Errorf("state provider is required")
	}

	// Update timestamps
	now := time.Now()
	if state.Metadata.CreatedAt.IsZero() {
		state.Metadata.CreatedAt = now
	}
	state.Metadata.UpdatedAt = now

	// Ensure provider directory exists
	providerPath := filepath.Join(s.basePath, state.Metadata.Provider)
	if err := os.MkdirAll(providerPath, 0o700); err != nil {
		return fmt.Errorf("failed to create provider directory: %w", err)
	}

	// Marshal and write state
	data, err := yaml.Marshal(state)
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	path := s.statePath(state.Metadata.Provider, state.Metadata.Name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("failed to write state: %w", err)
	}

	return nil
}

// Delete removes state from disk
func (s *Store) Delete(provider, name string) error {
	path := s.statePath(provider, name)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("failed to delete state: %w", err)
	}
	return nil
}

// Exists checks if state exists
func (s *Store) Exists(provider, name string) bool {
	path := s.statePath(provider, name)
	_, err := os.Stat(path)
	return err == nil
}

// statePath returns the file path for a state
func (s *Store) statePath(provider, name string) string {
	return filepath.Join(s.basePath, provider, name+".yaml")
}

// BasePath returns the base path of the store
func (s *Store) BasePath() string {
	return s.basePath
}

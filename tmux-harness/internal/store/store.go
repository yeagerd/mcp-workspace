// Package store is a thread-safe, JSON-file workspace registry.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// WorkspaceStatus describes the lifecycle state of a workspace.
type WorkspaceStatus string

const (
	StatusActive   WorkspaceStatus = "active"
	StatusArchived WorkspaceStatus = "archived"
	StatusOrphaned WorkspaceStatus = "orphaned"
)

// Workspace is a single registry entry.
type Workspace struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	TmuxSession     string            `json:"tmuxSession"`
	WorktreePath    string            `json:"worktreePath"`
	Branch          string            `json:"branch"`
	Status          WorkspaceStatus   `json:"status"`
	CreatedAt       time.Time         `json:"createdAt"`
	ArchivedAt      *time.Time        `json:"archivedAt,omitempty"`
	LastCaptureHash string            `json:"lastCaptureHash"`
	LastChangedAt   time.Time         `json:"lastChangedAt"`
	Meta            map[string]string `json:"meta,omitempty"`
}

// ErrNotFound is returned when a lookup by ID or name yields no result.
var ErrNotFound = errors.New("workspace not found")

// ErrNameConflict is returned when adding a workspace whose name already exists with status active.
var ErrNameConflict = errors.New("active workspace with that name already exists")

// Store is a concurrency-safe registry backed by a JSON file.
type Store struct {
	mu   sync.RWMutex
	path string
	data []Workspace
}

// NewStore creates or opens the registry at path.
// Parent directories are created if absent. If the file exists its contents are loaded.
func NewStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("creating store directory: %w", err)
	}

	s := &Store{path: path}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading store file: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &s.data); err != nil {
			return nil, fmt.Errorf("parsing store file (may be corrupt): %w", err)
		}
	}

	return s, nil
}

// Add inserts a new workspace. Rejects if an active workspace with the same name already exists.
// The workspace ID is generated here if it is empty.
func (s *Store) Add(ws Workspace) error {
	if ws.ID == "" {
		ws.ID = uuid.New().String()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, existing := range s.data {
		if existing.Name == ws.Name && existing.Status == StatusActive {
			return fmt.Errorf("%w: %s", ErrNameConflict, ws.Name)
		}
	}

	s.data = append(s.data, ws)
	return s.flush()
}

// Get returns a workspace by ID.
func (s *Store) Get(id string) (Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, ws := range s.data {
		if ws.ID == id {
			return ws, nil
		}
	}
	return Workspace{}, fmt.Errorf("%w: id=%s", ErrNotFound, id)
}

// GetByName returns a workspace by name (first active match, then any match).
func (s *Store) GetByName(name string) (Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var fallback *Workspace
	for i := range s.data {
		if s.data[i].Name == name {
			if s.data[i].Status == StatusActive {
				return s.data[i], nil
			}
			if fallback == nil {
				fallback = &s.data[i]
			}
		}
	}
	if fallback != nil {
		return *fallback, nil
	}
	return Workspace{}, fmt.Errorf("%w: name=%s", ErrNotFound, name)
}

// List returns all workspaces. If includeArchived is false, archived and orphaned are excluded.
func (s *Store) List(includeArchived bool) []Workspace {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]Workspace, 0, len(s.data))
	for _, ws := range s.data {
		if !includeArchived && ws.Status != StatusActive {
			continue
		}
		result = append(result, ws)
	}
	return result
}

// Update applies a mutation function to the workspace with the given ID and flushes.
func (s *Store) Update(id string, apply func(*Workspace)) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.data {
		if s.data[i].ID == id {
			apply(&s.data[i])
			return s.flush()
		}
	}
	return fmt.Errorf("%w: id=%s", ErrNotFound, id)
}

// Delete hard-deletes a workspace by ID from the JSON file.
// Normal lifecycle changes should use Update to set status instead.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, ws := range s.data {
		if ws.ID == id {
			s.data = append(s.data[:i], s.data[i+1:]...)
			return s.flush()
		}
	}
	return fmt.Errorf("%w: id=%s", ErrNotFound, id)
}

// flush writes the current data to a temp file then renames it atomically over the target.
// Must be called with s.mu already held.
func (s *Store) flush() error {
	tmp := s.path + ".tmp"

	out, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling store: %w", err)
	}

	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return fmt.Errorf("writing store temp file: %w", err)
	}

	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("renaming store temp file: %w", err)
	}

	return nil
}

package workspace

import (
	"fmt"
	"strings"
)

// Resolve returns the single workspace matching input by exact ID, exact name, ID prefix,
// or name prefix. All workspaces including archived are searched.
// Returns ErrNotFound if no workspace matches, ErrAmbiguous if more than one matches.
func (m *Manager) Resolve(input string) (Workspace, error) {
	all := m.store.List(true)

	var ids []string
	seen := make(map[string]bool)
	for _, sw := range all {
		if seen[sw.ID] {
			continue
		}
		if sw.ID == input || sw.Name == input ||
			strings.HasPrefix(sw.ID, input) || strings.HasPrefix(sw.Name, input) {
			ids = append(ids, sw.ID)
			seen[sw.ID] = true
		}
	}

	switch len(ids) {
	case 0:
		return Workspace{}, fmt.Errorf("%w: %s", ErrNotFound, input)
	case 1:
		sw, err := m.store.Get(ids[0])
		if err != nil {
			return Workspace{}, fmt.Errorf("%w: %s", ErrNotFound, input)
		}
		return m.buildWorkspace(sw), nil
	default:
		parts := make([]string, len(ids))
		for i, id := range ids {
			sw, _ := m.store.Get(id)
			parts[i] = fmt.Sprintf("%s (%s)", sw.Name, sw.ID)
		}
		return Workspace{}, fmt.Errorf("%w: %q matches [%s]", ErrAmbiguous, input, strings.Join(parts, ", "))
	}
}

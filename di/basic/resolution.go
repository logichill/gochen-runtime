package basic

import (
	"strings"

	"gochen/errors"
)

type resolutionState struct {
	stack []string
}

type resolutionWait struct {
	owner *resolutionState
	label string
}

func (s *resolutionState) push(label string) {
	s.stack = append(s.stack, label)
}

func (s *resolutionState) pop() {
	if len(s.stack) == 0 {
		return
	}
	s.stack = s.stack[:len(s.stack)-1]
}

func (s *resolutionState) cyclePath(label string) []string {
	for i, existing := range s.stack {
		if existing == label {
			path := append([]string(nil), s.stack[i:]...)
			path = append(path, label)
			return path
		}
	}
	return []string{label}
}

func (s *resolutionState) intersects(labels []string) bool {
	if s == nil {
		return false
	}
	for _, label := range labels {
		for _, existing := range s.stack {
			if existing == label {
				return true
			}
		}
	}
	return false
}

func (s *resolutionState) cyclePathFromAny(labels []string) []string {
	for _, label := range labels {
		for i, existing := range s.stack {
			if existing == label {
				path := append([]string(nil), s.stack[i:]...)
				path = append(path, label)
				return path
			}
		}
	}
	return append([]string(nil), labels...)
}

func (c *Container) withResolutionFrame(state *resolutionState, label string, fn func() (any, error)) (any, error) {
	if label == "" {
		return fn()
	}
	if state == nil {
		state = &resolutionState{}
	}
	if path := state.cyclePath(label); len(path) > 1 {
		return nil, newCircularDependencyError(path)
	}

	state.push(label)
	defer state.pop()

	return fn()
}

func newCircularDependencyError(path []string) error {
	return errors.NewCode(errors.Dependency, "circular dependency detected").
		WithContext("cycle", strings.Join(path, " -> "))
}

func (c *Container) beginResolutionWait(waiter, owner *resolutionState, label string) (func(), error) {
	if c == nil || waiter == nil || owner == nil {
		return func() {}, nil
	}

	c.resolutionWaitMu.Lock()
	defer c.resolutionWaitMu.Unlock()

	path := []string{label}
	current := owner
	visited := make(map[*resolutionState]struct{})
	for current != nil {
		if current == waiter {
			path = append(path, label)
			return nil, newCircularDependencyError(path)
		}
		if _, ok := visited[current]; ok {
			break
		}
		visited[current] = struct{}{}

		edge, ok := c.resolutionWaits[current]
		if !ok {
			break
		}
		path = append(path, edge.label)
		current = edge.owner
	}

	c.resolutionWaits[waiter] = resolutionWait{owner: owner, label: label}
	return func() {
		c.resolutionWaitMu.Lock()
		edge, ok := c.resolutionWaits[waiter]
		if ok && edge.owner == owner && edge.label == label {
			delete(c.resolutionWaits, waiter)
		}
		c.resolutionWaitMu.Unlock()
	}, nil
}

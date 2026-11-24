package discovery

import (
	"os/user"
	"sync"
)

// account is a user's name and home directory.
type account struct{ name, home string }

// accounts caches user lookups for one scanner.
type accounts struct {
	mu    sync.Mutex
	known map[string]account
}

// lookup resolves a user ID, empty when unknown.
func (a *accounts) lookup(id string) account {
	if id == "" {
		return account{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if found, ok := a.known[id]; ok {
		return found
	}
	if a.known == nil {
		a.known = map[string]account{}
	}
	var found account
	if entry, err := user.LookupId(id); err == nil {
		found = account{name: entry.Username, home: entry.HomeDir}
	}
	a.known[id] = found
	return found
}

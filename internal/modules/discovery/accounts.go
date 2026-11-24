package discovery

import (
	"os/user"
	"sync"
	"time"
)

// A domain controller may be unreachable
const userDeadline = 300 * time.Millisecond

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
	found := lookupUser(id)
	a.known[id] = found
	return found
}

// lookupUser gives up on directory services that do not answer.
func lookupUser(id string) account {
	answer := make(chan account, 1)
	go func() {
		entry, err := user.LookupId(id)
		if err != nil {
			answer <- account{}
			return
		}
		answer <- account{name: entry.Username, home: entry.HomeDir}
	}()
	select {
	case found := <-answer:
		return found
	case <-time.After(userDeadline):
		return account{}
	}
}

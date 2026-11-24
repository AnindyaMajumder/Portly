// Package docker talks to local Docker and Podman engines.
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// Responses above this are cut, which makes them malformed
const maxResponse = 16 << 20

// endpoint is one verified local socket or pipe.
type endpoint struct {
	path     string
	ownerUID string
}

// Client finds engines and their containers.
type Client struct {
	environment environment
	caller      int
}

// New returns a client for the caller's engines.
func New() *Client {
	return &Client{environment: callerEnvironment(), caller: os.Geteuid()}
}

// Engines asks every local engine in parallel. Absent ones are left out.
func (c *Client) Engines(ctx context.Context) ([]domain.Engine, []string) {
	paths, warnings := c.environment.localPaths()
	answers := make([]domain.Engine, len(paths))
	var workers sync.WaitGroup
	for i, path := range paths {
		workers.Go(func() { answers[i] = c.ask(ctx, path) })
	}
	workers.Wait()
	var engines []domain.Engine
	seen := map[string]bool{}
	for _, engine := range answers {
		if engine.Answer != domain.AnswerAbsent && !seen[engine.ID] {
			seen[engine.ID] = true
			engines = append(engines, engine)
		}
	}
	return engines, warnings
}

// ask lists one engine's containers. A silent engine keeps its owner.
func (c *Client) ask(ctx context.Context, path string) domain.Engine {
	end, answer := openEndpoint(path, c.caller)
	if answer != domain.AnswerOK {
		return domain.Engine{ID: path, OwnerUID: end.ownerUID, Answer: answer}
	}
	engine := domain.Engine{ID: end.path, OwnerUID: end.ownerUID}
	api := newAPI(end)
	defer api.close()
	engine.Kind, engine.Containers, engine.Answer = api.list(ctx)
	for i := range engine.Containers {
		engine.Containers[i].Engine = engine.ID
	}
	return engine
}

// api is an HTTP connection to one engine.
type api struct {
	http      *http.Client
	transport *http.Transport
}

// newAPI never follows redirects and never leaves the socket.
func newAPI(end endpoint) *api {
	transport := &http.Transport{
		DialContext:        func(ctx context.Context, _, _ string) (net.Conn, error) { return end.dial(ctx) },
		DisableCompression: true,
	}
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &api{http: client, transport: transport}
}

// close drops idle connections.
func (a *api) close() { a.transport.CloseIdleConnections() }

// statusError is an unexpected HTTP status.
type statusError int

// Error renders the status.
func (s statusError) Error() string { return fmt.Sprintf("engine answered %d", int(s)) }

// get decodes one bounded JSON response and returns its headers.
func (a *api) get(ctx context.Context, path string, into any) (http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://engine"+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build engine request: %w", err)
	}
	response, err := a.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("ask engine: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, statusError(response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponse)).Decode(into); err != nil {
		return nil, fmt.Errorf("decode engine answer: %w", err)
	}
	return response.Header, nil
}

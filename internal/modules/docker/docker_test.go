//go:build unix

package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

var (
	dbID    = strings.Repeat("a", 64)
	sideID  = strings.Repeat("b", 64)
	started = "2026-11-24T10:00:00.5Z"
)

// fakeEngine is an HTTP server on a socket in a temporary directory.
type fakeEngine struct {
	mu       sync.Mutex
	path     string
	version  string
	list     string
	inspect  string
	status   int
	requests []string
	onPost   func(http.ResponseWriter, *http.Request)
}

// newFakeEngine serves one published container.
func newFakeEngine(t *testing.T) *fakeEngine {
	t.Helper()
	dir, err := os.MkdirTemp("", "portly")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	engine := &fakeEngine{
		path: filepath.Join(dir, "e.sock"), version: `{"Components":[{"Name":"Engine"}]}`, status: http.StatusNoContent,
		list: fmt.Sprintf(`[{"Id":%q,"Names":["/db"],"Image":"redis","State":"running","Labels":{"com.docker.compose.project":"shop",
			"com.docker.compose.service":"db","com.docker.compose.project.working_dir":"/code/shop"},"HostConfig":{"NetworkMode":"bridge"},
			"Ports":[{"IP":"127.0.0.1","PrivatePort":80,"PublicPort":8000,"Type":"tcp"},{"PrivatePort":90,"Type":"tcp"}]},
			{"Id":%q,"Names":["/side"],"State":"running","HostConfig":{"NetworkMode":"container:%s"}}]`, dbID, sideID, dbID),
	}
	engine.setInspect(started, "true", "false")
	listener, err := net.Listen("unix", engine.path)
	if err != nil {
		t.Fatal(err)
	}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: engine}}
	server.Start()
	t.Cleanup(server.Close)
	return engine
}

// setInspect changes what inspect answers.
func (e *fakeEngine) setInspect(startedAt, running, paused string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inspect = fmt.Sprintf(`{"Id":%q,"State":{"Running":%s,"Paused":%s,"Pid":900,"StartedAt":%q},"Config":{"StopSignal":"SIGINT"},"HostConfig":{"AutoRemove":true}}`, dbID, running, paused, startedAt)
}

// ServeHTTP records every request.
func (e *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	e.requests = append(e.requests, r.Method+" "+r.URL.RequestURI())
	version, list, inspect, status, onPost := e.version, e.list, e.inspect, e.status, e.onPost
	e.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && onPost != nil:
		onPost(w, r)
	case r.Method == http.MethodPost:
		w.WriteHeader(status)
	case r.URL.Path == "/version":
		_, _ = w.Write([]byte(version))
	case r.URL.Path == "/containers/json":
		_, _ = w.Write([]byte(list))
	case r.URL.Path == "/containers/"+dbID+"/json" && inspect != "":
		_, _ = w.Write([]byte(inspect))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// posts lists the POST requests received.
func (e *fakeEngine) posts() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var found []string
	for _, request := range e.requests {
		if strings.HasPrefix(request, "POST") {
			found = append(found, request)
		}
	}
	return found
}

// client uses only the fake engine, never a real socket.
func (e *fakeEngine) client() *Client {
	root := filepath.Join(filepath.Dir(e.path), "nothing")
	return &Client{environment: environment{host: "unix://" + e.path, root: root, configDir: root}, caller: os.Geteuid()}
}

// approval builds an approval for the fake container.
func (e *fakeEngine) approval(t *testing.T, method domain.Method) *domain.Approval {
	t.Helper()
	target := domain.StopTarget{Kind: domain.KindContainer, Container: domain.ContainerRef{Engine: e.path, ID: dbID, StartedAt: started}}
	approval, err := domain.Approve(target, target, method)
	if err != nil {
		t.Fatal(err)
	}
	return approval
}

// TestEngines reads containers, ports, labels and shared networks.
func TestEngines(t *testing.T) {
	engine := newFakeEngine(t)
	engines, warnings := engine.client().Engines(t.Context())
	if len(engines) != 1 || len(warnings) != 0 || engines[0].Answer != domain.AnswerOK || engines[0].Kind != "docker" {
		t.Fatalf("got %+v %v", engines, warnings)
	}
	if engines[0].ID != engine.path || engines[0].OwnerUID != fmt.Sprint(os.Geteuid()) {
		t.Errorf("identity %+v", engines[0])
	}
	got := engines[0].Containers[0]
	if got.Name != "db" || got.ComposeProject != "shop" || got.ComposeService != "db" || got.Directory != "/code/shop" || got.Engine != engine.path {
		t.Errorf("names %+v", got)
	}
	if !got.Inspected || got.StartedAt != started || got.InitPID != 900 || got.StopSignal != "SIGINT" || !got.AutoRemove || !got.Running {
		t.Errorf("state %+v", got)
	}
	if len(got.Ports) != 1 || got.Ports[0].HostPort != 8000 || got.Ports[0].ContainerPort != 80 || !slices.Equal(got.SharesNet, []string{"side"}) {
		t.Errorf("network %+v %v", got.Ports, got.SharesNet)
	}
	if len(engine.posts()) != 0 {
		t.Error("listing must only read")
	}
}

// TestPodmanIsRecognized by its version component.
func TestPodmanIsRecognized(t *testing.T) {
	engine := newFakeEngine(t)
	engine.version = `{"Components":[{"Name":"Podman Engine"}]}`
	if engines, _ := engine.client().Engines(t.Context()); engines[0].Kind != "podman" {
		t.Errorf("got %+v", engines[0])
	}
}

// TestHostileAnswers never panic and are never called OK.
func TestHostileAnswers(t *testing.T) {
	bad := map[string]func(*fakeEngine){
		"version":  func(e *fakeEngine) { e.version = "<html>" },
		"list":     func(e *fakeEngine) { e.list = "{" },
		"id":       func(e *fakeEngine) { e.list = `[{"Id":"../../etc"}]` },
		"short id": func(e *fakeEngine) { e.list = `[{"Id":"abc"}]` },
		"address": func(e *fakeEngine) {
			e.list = fmt.Sprintf(`[{"Id":%q,"Ports":[{"IP":"x","PublicPort":1,"Type":"tcp"}]}]`, dbID)
		},
		"port": func(e *fakeEngine) {
			e.list = fmt.Sprintf(`[{"Id":%q,"Ports":[{"PublicPort":70000,"Type":"tcp"}]}]`, dbID)
		},
		"too large":  func(e *fakeEngine) { e.list = `["` + strings.Repeat("x", maxResponse) + `"]` },
		"wrong type": func(e *fakeEngine) { e.list = `{"message":"no"}` },
	}
	for name, edit := range bad {
		engine := newFakeEngine(t)
		edit(engine)
		engines, _ := engine.client().Engines(t.Context())
		if engines[0].Answer != domain.AnswerMalformed || len(engines[0].Containers) != 0 {
			t.Errorf("%s: %+v", name, engines[0])
		}
	}
}

// TestUnverifiedInspect leaves identity unknown.
func TestUnverifiedInspect(t *testing.T) {
	for name, text := range map[string]string{
		"missing": "", "other container": strings.Replace(`{"Id":"X","State":{"StartedAt":"2026-11-24T10:00:00Z"}}`, "X", sideID, 1), "no start": fmt.Sprintf(`{"Id":%q}`, dbID),
	} {
		engine := newFakeEngine(t)
		engine.inspect = text
		engines, _ := engine.client().Engines(t.Context())
		if got := engines[0].Containers[0]; got.Inspected || got.StartedAt != "" {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

// TestOrchestrators are recognized by label.
func TestOrchestrators(t *testing.T) {
	for label, want := range map[string]string{
		"com.docker.swarm.service.id": "swarm", "io.kubernetes.pod.name": "kubernetes", "io.x-k8s.kind.cluster": "kubernetes",
		"PODMAN_SYSTEMD_UNIT": "systemd", "com.docker.compose.project": "",
	} {
		if got := orchestrator(map[string]string{label: "x"}); got != want {
			t.Errorf("%s: %q", label, got)
		}
	}
}

// TestSilentEngines keeps the reason and the owner.
func TestSilentEngines(t *testing.T) {
	engine := newFakeEngine(t)
	listener, err := net.Listen("unix", filepath.Join(filepath.Dir(engine.path), "hung.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client := engine.client()
	client.environment.host = "unix://" + listener.Addr().String()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	engines, _ := client.Engines(ctx)
	if len(engines) != 1 || engines[0].Answer != domain.AnswerTimeout || engines[0].OwnerUID == "" {
		t.Errorf("hung: %+v", engines)
	}
	dead := filepath.Join(filepath.Dir(engine.path), "dead.sock")
	closed, err := net.ListenUnix("unix", &net.UnixAddr{Name: dead})
	if err != nil {
		t.Fatal(err)
	}
	closed.SetUnlinkOnClose(false)
	_ = closed.Close()
	client.environment.host = "unix://" + dead
	if engines, _ := client.Engines(t.Context()); len(engines) != 1 || engines[0].Answer != domain.AnswerRefused {
		t.Errorf("dead: %+v", engines)
	}
	client.environment.host = "unix://" + dead + ".missing"
	if engines, _ := client.Engines(t.Context()); len(engines) != 0 {
		t.Errorf("missing: %+v", engines)
	}
}

// TestUntrustedSockets are not asked.
func TestUntrustedSockets(t *testing.T) {
	engine := newFakeEngine(t)
	client := engine.client()
	client.caller = os.Geteuid() + 1
	if os.Geteuid() != 0 {
		if engines, _ := client.Engines(t.Context()); len(engines) != 0 || len(engine.requests) != 0 {
			t.Errorf("another user's socket was asked: %+v", engines)
		}
	}
	file := filepath.Join(filepath.Dir(engine.path), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, answer := openEndpoint(file, os.Geteuid()); answer != domain.AnswerAbsent {
		t.Errorf("a file is not a socket: %v", answer)
	}
}

// TestEndpointOrder covers host, context, remote hosts and repeats.
func TestEndpointOrder(t *testing.T) {
	config := t.TempDir()
	digest := sha256.Sum256([]byte("work"))
	meta := filepath.Join(config, "contexts/meta", hex.EncodeToString(digest[:]))
	if err := os.MkdirAll(meta, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, text string) {
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(meta, "meta.json"), `{"Endpoints":{"docker":{"Host":"unix:///ctx.sock"}}}`)
	write(filepath.Join(config, "config.json"), `{"currentContext":"work"}`)
	paths, _ := environment{host: "unix:///host.sock", configDir: config, root: "/"}.localPaths()
	if paths[0] != "/host.sock" || paths[1] != "/ctx.sock" || !slices.Contains(paths, "/var/run/docker.sock") {
		t.Errorf("got %v", paths)
	}
	paths, warnings := environment{host: "tcp://10.0.0.1:2375", context: "default", configDir: config, root: "/"}.localPaths()
	if len(warnings) != 1 || paths[0] != "/var/run/docker.sock" {
		t.Errorf("got %v %v", paths, warnings)
	}
	if path, isLocal := localPath("npipe:////./pipe/docker_engine"); !isLocal || path != `\\.\pipe\docker_engine` {
		t.Errorf("pipe: %q %v", path, isLocal)
	}
	for _, host := range []string{"npipe:////server/pipe/docker_engine", "npipe://x", "", "tcp://x", "ssh://x", "unix://relative", "http://x"} {
		if _, isLocal := localPath(host); isLocal {
			t.Errorf("%q is not local", host)
		}
	}
	if got := distinctPaths([]string{"/a", "", "/b", "/a"}); !slices.Equal(got, []string{"/a", "/b"}) {
		t.Errorf("got %v", got)
	}
	broken := environment{context: "missing", configDir: config}
	if broken.contextHost() != "" {
		t.Error("a missing context has no host")
	}
}

// TestStopUsesNoTimeout sends exactly one t=-1 request.
func TestStopUsesNoTimeout(t *testing.T) {
	engine := newFakeEngine(t)
	if got := engine.client().Stop(t.Context(), engine.approval(t, domain.Graceful)); !got.Accepted {
		t.Errorf("got %+v", got)
	}
	if want := []string{"POST /containers/" + dbID + "/stop?t=-1"}; !slices.Equal(engine.posts(), want) {
		t.Errorf("got %v", engine.posts())
	}
}

// TestForceStopKills sends exactly one kill.
func TestForceStopKills(t *testing.T) {
	engine := newFakeEngine(t)
	if got := engine.client().ForceStop(t.Context(), engine.approval(t, domain.Forced)); !got.Accepted {
		t.Errorf("got %+v", got)
	}
	if want := []string{"POST /containers/" + dbID + "/kill?signal=SIGKILL"}; !slices.Equal(engine.posts(), want) {
		t.Errorf("got %v", engine.posts())
	}
}

// TestRejectedStopIsNeverRetried covers every engine answer.
func TestRejectedStopIsNeverRetried(t *testing.T) {
	for status, want := range map[int]domain.Code{
		304: domain.CodeChanged, 307: domain.CodeUnknownOutcome, 400: domain.CodeEngineRejected, 403: domain.CodeEngineRejected,
		404: domain.CodeChanged, 409: domain.CodeChanged, 500: domain.CodeUnknownOutcome, 503: domain.CodeUnknownOutcome,
	} {
		engine := newFakeEngine(t)
		engine.status = status
		got := engine.client().Stop(t.Context(), engine.approval(t, domain.Graceful))
		if got.Accepted || got.Code != want || len(engine.posts()) != 1 {
			t.Errorf("%d: %+v after %v", status, got, engine.posts())
		}
	}
}

// TestChangedContainerIsNotTouched covers changes right before the request.
func TestChangedContainerIsNotTouched(t *testing.T) {
	cases := map[string]struct {
		edit func(*fakeEngine)
		want domain.Code
	}{
		"restarted": {func(e *fakeEngine) { e.setInspect("2026-11-24T11:00:00Z", "true", "false") }, domain.CodeChanged},
		"exited":    {func(e *fakeEngine) { e.setInspect(started, "false", "false") }, domain.CodeChanged},
		"paused":    {func(e *fakeEngine) { e.setInspect(started, "true", "true") }, domain.CodeChanged},
		"removed":   {func(e *fakeEngine) { e.inspect = "" }, domain.CodeChanged},
		"malformed": {func(e *fakeEngine) { e.inspect = "{" }, domain.CodeEngineUnresponsive},
	}
	for name, c := range cases {
		engine := newFakeEngine(t)
		c.edit(engine)
		for _, got := range []domain.Delivery{
			engine.client().Stop(t.Context(), engine.approval(t, domain.Graceful)),
			engine.client().ForceStop(t.Context(), engine.approval(t, domain.Forced)),
		} {
			if got.Accepted || got.Code != c.want || len(engine.posts()) != 0 {
				t.Errorf("%s: %+v after %v", name, got, engine.posts())
			}
		}
	}
}

// TestStopNeedsTheRightApproval covers wrong methods, kinds and reuse.
func TestStopNeedsTheRightApproval(t *testing.T) {
	engine := newFakeEngine(t)
	client := engine.client()
	used := engine.approval(t, domain.Graceful)
	_, _ = used.Take(domain.Graceful)
	for name, got := range map[string]domain.Delivery{
		"nil": client.Stop(t.Context(), nil), "zero": client.ForceStop(t.Context(), &domain.Approval{}),
		"used": client.Stop(t.Context(), used), "wrong method": client.ForceStop(t.Context(), engine.approval(t, domain.Graceful)),
	} {
		if got.Accepted {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(engine.posts()) != 0 {
		t.Errorf("requests %v", engine.posts())
	}
}

// TestWrittenRequestThatTimesOutWasAccepted needs our own deadline, not a failure.
func TestWrittenRequestThatTimesOutWasAccepted(t *testing.T) {
	engine := newFakeEngine(t)
	release := make(chan struct{})
	engine.onPost = func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}
	end, _ := openEndpoint(engine.path, os.Geteuid())
	api := newAPI(end)
	defer api.close()
	if got := api.post(t.Context(), "/containers/x/wait", 100*time.Millisecond); !got.Accepted {
		t.Errorf("our timeout: %+v", got)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if got := api.post(ctx, "/containers/x/wait", time.Minute); got.Accepted || got.Code != domain.CodeUnknownOutcome {
		t.Errorf("cancelled by the caller: %+v", got)
	}
	close(release)
	engine.mu.Lock()
	engine.onPost = func(w http.ResponseWriter, _ *http.Request) { panic(http.ErrAbortHandler) }
	engine.mu.Unlock()
	if got := api.post(t.Context(), "/containers/x/wait", time.Second); got.Accepted {
		t.Errorf("a dropped connection is not accepted: %+v", got)
	}
	missing := newAPI(endpoint{path: engine.path + ".missing"})
	if got := missing.post(t.Context(), "/containers/x/wait", time.Second); got.Code != domain.CodeEngineUnresponsive {
		t.Errorf("never written: %+v", got)
	}
}

// TestState tells running, stopped, gone and unknown apart.
func TestState(t *testing.T) {
	engine := newFakeEngine(t)
	ref := domain.ContainerRef{Engine: engine.path, ID: dbID, StartedAt: started}
	if got := engine.client().State(t.Context(), ref); got != (domain.ContainerState{Known: true, Running: true, StartedAt: started}) {
		t.Errorf("running: %+v", got)
	}
	engine.inspect = ""
	if got := engine.client().State(t.Context(), ref); got != (domain.ContainerState{Known: true}) {
		t.Errorf("gone: %+v", got)
	}
	engine.inspect = "{"
	if got := engine.client().State(t.Context(), ref); got.Known {
		t.Errorf("malformed: %+v", got)
	}
	ref.Engine += ".missing"
	if got := engine.client().State(t.Context(), ref); got.Known {
		t.Errorf("no engine: %+v", got)
	}
}

// FuzzListed proves no container entry panics or keeps a bad ID.
func FuzzListed(f *testing.F) {
	f.Add(dbID, "127.0.0.1", 8000, "tcp")
	f.Fuzz(func(t *testing.T, id, ip string, port int, kind string) {
		entry := listed{ID: id}
		entry.Ports = append(entry.Ports, struct {
			IP                      string
			PrivatePort, PublicPort int
			Type                    string
		}{ip, port, port, kind})
		container, err := entry.container()
		if err == nil && !containerID.MatchString(container.ID) {
			t.Fatalf("accepted %q", id)
		}
	})
}

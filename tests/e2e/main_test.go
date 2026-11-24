//go:build linux

// Package e2e runs the real binary against servers the tests start.
package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	modeVariable = "PORTLY_E2E_MODE"
	// Every server ends by itself, even if its test crashes
	serverLifetime = 90 * time.Second
)

var (
	// The binary users get, and one that may stop this test's children
	release, evidence string
)

// TestMain builds both binaries, or becomes a server when asked.
func TestMain(m *testing.M) {
	if mode := os.Getenv(modeVariable); mode != "" {
		serve(mode)
		return
	}
	// Orphans of a test stay its descendants, so they can be cleaned up
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		fmt.Fprintln(os.Stderr, "subreaper:", err)
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "portly-e2e")
	if err == nil {
		release, err = build(dir, "portly")
	}
	if err == nil {
		evidence, err = build(dir, "portly-evidence", "-tags", "portly_evidence")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// build compiles one binary into a temporary directory.
func build(dir, name string, flags ...string) (string, error) {
	path := filepath.Join(dir, name)
	arguments := append([]string{"build", "-o", path}, flags...)
	command := exec.Command("go", append(arguments, "../../cmd/portly")...)
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, output)
	}
	return path, nil
}

// server is a child process listening on one or more ports.
type server struct {
	t       *testing.T
	command *exec.Cmd
	ports   []string
	exited  chan struct{}
}

// start launches a server and waits for its ports.
func start(t *testing.T, mode string, extra ...string) *server {
	t.Helper()
	command := exec.Command(os.Args[0])
	command.Env = append(append(os.Environ(), modeVariable+"="+mode), extra...)
	return launch(t, command)
}

// launch starts any command that prints its ports, and kills only that child.
func launch(t *testing.T, command *exec.Cmd) *server {
	t.Helper()
	command.Stderr = os.Stderr
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	s := &server{t: t, command: command, exited: make(chan struct{})}
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil {
		t.Fatalf("server did not start: %v", err)
	}
	s.ports = strings.Fields(line)
	go func() {
		_ = command.Wait()
		close(s.exited)
	}()
	t.Cleanup(func() {
		_ = command.Process.Kill()
		<-s.exited
		reapOrphans()
	})
	return s
}

// reapOrphans ends leftover descendants that were reparented to this test.
func reapOrphans() {
	for range 20 {
		data, _ := os.ReadFile(fmt.Sprintf("/proc/self/task/%d/children", os.Getpid()))
		for _, field := range strings.Fields(string(data)) {
			pid, err := strconv.Atoi(field)
			if err == nil && pid > 1 && isServer(pid) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				_, _ = syscall.Wait4(pid, nil, 0, nil)
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// isServer recognizes only this test binary running as a server.
func isServer(pid int) bool {
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	self, errSelf := os.Executable()
	return err == nil && errSelf == nil && exe == self
}

// alive reports whether the server itself still runs.
func (s *server) alive() bool {
	select {
	case <-s.exited:
		return false
	default:
		return true
	}
}

// pid is the server's process ID.
func (s *server) pid() int { return s.command.Process.Pid }

// result is one run of portly.
type result struct {
	exit   int
	stdout string
	stderr string
	json   document
}

// document is the part of the JSON output the tests check.
type document struct {
	PlanID  string `json:"plan_id"`
	Error   *problem
	Ports   []map[string]any
	Targets []struct {
		Kind     string
		Affected []struct{ Port int }
		Children []struct{ PID int }
		Outcome  *struct {
			Status, Method string
			Resumed        bool
			KilledPIDs     []int `json:"killed_pids"`
		}
	}
	Results []struct {
		Port     int
		Protocol string
		Status   string
		Error    *problem
	}
}

// problem is an error in the JSON output.
type problem struct{ Code, Message, Hint string }

// portly runs a binary without a terminal.
func portly(t *testing.T, binary string, args ...string) result {
	t.Helper()
	var stdout, stderr strings.Builder
	command := exec.Command(binary, args...)
	command.Env = append(os.Environ(), environment(t)...)
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	var exited *exec.ExitError
	if err != nil && !errors.As(err, &exited) {
		t.Fatal(err)
	}
	found := result{exit: command.ProcessState.ExitCode(), stdout: stdout.String(), stderr: stderr.String()}
	if strings.HasPrefix(found.stdout, "{") {
		if err := json.Unmarshal([]byte(found.stdout), &found.json); err != nil {
			t.Fatalf("%v\n%s", err, found.stdout)
		}
	}
	return found
}

// closedEngine adds an engine the caller may not reach, for one test.
var closedEngine = map[string]bool{}

// environment points a chosen test's Portly at a closed directory.
func environment(t *testing.T) []string {
	if closedEngine[t.Name()] {
		return []string{"DOCKER_HOST=unix:///root/portly-test/engine.sock"}
	}
	return nil
}

// stop runs a confirmed stop with the evidence build.
func stop(t *testing.T, args ...string) result {
	t.Helper()
	return portly(t, evidence, append([]string{"stop", "--yes", "--json"}, args...)...)
}

// expect fails unless exit status and first result match.
func (r result) expect(t *testing.T, exit int, status string) {
	t.Helper()
	if r.exit != exit || len(r.json.Results) == 0 || r.json.Results[0].Status != status {
		t.Fatalf("exit %d, want %d %s\n%s%s", r.exit, exit, status, r.stdout, r.stderr)
	}
}

// isFree reports whether a TCP port can be bound again.
func isFree(port string) bool {
	listener, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// serve is the server side of every test.
func serve(mode string) {
	go func() {
		time.Sleep(serverLifetime)
		os.Exit(9)
	}()
	switch mode {
	case "tcp":
		announce(listen("127.0.0.1:0"))
	case "ignore":
		signal.Ignore(syscall.SIGTERM)
		announce(listen("127.0.0.1:0"))
	case "two-ports":
		announce(listen("127.0.0.1:0"), listen("127.0.0.1:0"))
	case "dual":
		first := listen("127.0.0.1:0")
		announce(first, listen("[::1]:"+portOf(first)))
	case "udp":
		conn, err := net.ListenPacket("udp", "127.0.0.1:"+os.Getenv("PORTLY_E2E_PORT"))
		check(err)
		_, port, _ := net.SplitHostPort(conn.LocalAddr().String())
		fmt.Println(port)
	case "share":
		announce(reuse("127.0.0.1:" + os.Getenv("PORTLY_E2E_PORT")))
	case "family", "stubborn-family":
		serveFamily(mode == "stubborn-family")
	case "worker":
		serveWorker()
	case "respawn":
		serveRespawn()
	case "parent":
		serveParent()
	}
	select {}
}

// check ends a server that can't run.
func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// listen opens a TCP port.
func listen(address string) net.Listener {
	listener, err := net.Listen("tcp", address)
	check(err)
	return listener
}

// reuse opens a TCP port that another process may share.
func reuse(address string) net.Listener {
	config := net.ListenConfig{Control: func(_, _ string, conn syscall.RawConn) error {
		return conn.Control(func(fd uintptr) { check(unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)) })
	}}
	listener, err := config.Listen(context.Background(), "tcp", address)
	check(err)
	return listener
}

// portOf is the port of a listener.
func portOf(listener net.Listener) string {
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	return port
}

// announce prints every port on one line.
func announce(listeners ...net.Listener) {
	var ports []string
	for _, listener := range listeners {
		ports = append(ports, portOf(listener))
	}
	fmt.Println(strings.Join(ports, " "))
}

// child starts this binary in another mode, with its output on stderr.
func child(mode string, extra ...string) *exec.Cmd {
	command := exec.Command(os.Args[0])
	command.Env = append(append(os.Environ(), modeVariable+"="+mode), extra...)
	command.Stderr = os.Stderr
	return command
}

// serveFamily is a prefork server with two workers on one socket.
func serveFamily(isStubborn bool) {
	listener := listen("127.0.0.1:0")
	file, err := listener.(*net.TCPListener).File()
	check(err)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	var workers []*exec.Cmd
	for range 2 {
		worker := child("worker")
		worker.ExtraFiles = []*os.File{file}
		check(worker.Start())
		workers = append(workers, worker)
	}
	announce(listener)
	for range stop {
		if isStubborn {
			continue
		}
		for _, worker := range workers {
			_ = worker.Process.Kill()
			_ = worker.Wait()
		}
		os.Exit(0)
	}
}

// serveWorker keeps an inherited socket open.
func serveWorker() {
	signal.Ignore(syscall.SIGTERM)
	file := os.NewFile(3, "listener")
	_, err := net.FileListener(file)
	check(err)
}

// serveRespawn restarts its child whenever it ends.
func serveRespawn() {
	port := ""
	for {
		server := child("fixed", "PORTLY_E2E_PORT="+port)
		server.Env = append(server.Env, modeVariable+"=share")
		output, err := server.StdoutPipe()
		check(err)
		check(server.Start())
		line, _ := bufio.NewReader(output).ReadString('\n')
		if port == "" {
			fmt.Print(line)
		}
		port = strings.TrimSpace(line)
		_ = server.Wait()
	}
}

// serveParent runs one listening child under a chosen name.
func serveParent() {
	server := child("tcp")
	server.Stdout = os.Stdout
	check(server.Start())
	_ = server.Wait()
	os.Exit(0)
}

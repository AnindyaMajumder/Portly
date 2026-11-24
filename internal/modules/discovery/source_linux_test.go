package discovery

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

var port8000 = domain.Endpoint{Port: 8000, Protocol: domain.TCP}

// scanFake scans a fake tree and fails the test on error.
func scanFake(t *testing.T, fake fakeProc) domain.Snapshot {
	t.Helper()
	snapshot, err := fake.source().scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// TestScanJoinsSocketAndProcess covers the full happy path.
func TestScanJoinsSocketAndProcess(t *testing.T) {
	fake := newFakeProc(t)
	fake.listen(8000, "1000", "77")
	fake.process(fakeProcess{pid: 600, ppid: 1, uid: "1000", start: 60, inodes: []string{"77"}})
	fake.process(fakeProcess{pid: 601, ppid: 600, uid: "1000", start: 61})
	snapshot := scanFake(t, fake)
	sockets := snapshot.On(port8000)
	if len(sockets) != 1 || !slices.Equal(sockets[0].Holders, []domain.Holder{{PID: 600, FD: 3}}) {
		t.Fatalf("got %+v", sockets)
	}
	if got := sockets[0].Completeness(); got != domain.Complete {
		t.Errorf("completeness %v", got)
	}
	process := snapshot.Processes[600]
	want := domain.ProcessID{PID: 600, Start: "60", UserID: "1000", Exe: "/usr/bin/app"}
	if process.ProcessID != want || process.Dir != "/home/dev/app" || process.Caught != domain.Yes || process.TTY != domain.Yes || len(process.Args) != 3 {
		t.Errorf("got %+v", process)
	}
	if child := snapshot.Processes[601]; child.UserID != "1000" || child.Sockets != domain.Complete {
		t.Errorf("descendants need details: %+v", child)
	}
	if !snapshot.Processes[1].UserManager {
		t.Error("PID 1's scope is a service manager")
	}
}

// TestHostFacts covers the facts policy needs about this machine.
func TestHostFacts(t *testing.T) {
	host := newFakeProc(t).source().host()
	want := domain.Host{
		SelfPID: 500, CallerUID: "1000", SystemUIDMax: 999, InContainer: domain.No,
		ServiceManager: domain.Yes, ProcessTable: domain.Complete, Ephemeral: domain.Range{First: 32768, Last: 60999},
	}
	if host != want {
		t.Errorf("got %+v, want %+v", host, want)
	}
}

// TestHostInsideContainer covers visible, hidden and unclear container IDs.
func TestHostInsideContainer(t *testing.T) {
	id := strings.Repeat("d", 64)
	visible := newFakeProc(t)
	visible.write("proc/self/cgroup", "0::/docker/"+id+"\n")
	hidden := newFakeProc(t)
	hidden.write("proc/self/cgroup", "0::/\n")
	hidden.write("proc/self/mountinfo", "1 2 0:1 /var/lib/docker/containers/"+id+"/hostname /etc/hostname rw\n")
	for name, fake := range map[string]fakeProc{"visible": visible, "hidden": hidden} {
		if host := fake.source().host(); host.SelfContainer != id || host.InContainer != domain.Yes {
			t.Errorf("%s: %+v", name, host)
		}
	}
	unclear := newFakeProc(t)
	unclear.write("proc/self/cgroup", "0::/\n")
	if err := os.RemoveAll(filepath.Join(unclear.dir, "run")); err != nil {
		t.Fatal(err)
	}
	if host := unclear.source().host(); host.InContainer != domain.Unknown {
		t.Errorf("unclear: %+v", host)
	}
	unclear.link("proc/self/ns/pid", initialPIDNamespace)
	if host := unclear.source().host(); host.InContainer != domain.Unknown {
		t.Errorf("one namespace proves nothing: %+v", host)
	}
	unclear.link("proc/self/ns/cgroup", initialCgroupNamespace)
	if host := unclear.source().host(); host.InContainer != domain.No {
		t.Errorf("a host without systemd: %+v", host)
	}
	unclear.write(".dockerenv", "")
	if host := unclear.source().host(); host.InContainer != domain.Yes || host.SelfContainer != "" {
		t.Errorf("marker: %+v", host)
	}
}

// TestEveryProcessHasContainers covers processes that hold nothing here.
func TestEveryProcessHasContainers(t *testing.T) {
	fake, id := newFakeProc(t), strings.Repeat("e", 64)
	fake.process(fakeProcess{pid: 800, ppid: 1, uid: "0", start: 80, cgroup: "/system.slice/docker-" + id + ".scope"})
	found := scanFake(t, fake).Processes[800]
	if !slices.Equal(found.ContainerIDs, []string{id}) || found.ContainerPath != "/system.slice/docker-"+id+".scope" {
		t.Errorf("got %v %q", found.ContainerIDs, found.ContainerPath)
	}
}

// TestUnreadableDescriptorsAreNotComplete covers hidden and denied descriptors.
func TestUnreadableDescriptorsAreNotComplete(t *testing.T) {
	fake := newFakeProc(t)
	fake.listen(8000, "1000", "77")
	fake.process(fakeProcess{pid: 600, ppid: 1, uid: "1000", start: 60, inodes: []string{"77"}})
	fake.process(fakeProcess{pid: 700, ppid: 600, uid: "0", start: 70, noFDs: true})
	// A descriptor that can't be read as a link, like systemd --user
	fake.process(fakeProcess{pid: 701, ppid: 600, uid: "1000", start: 71})
	fake.write("proc/701/fd/9", "not a link")
	snapshot := scanFake(t, fake)
	if got := snapshot.Processes[700].Sockets; got != domain.Unavailable {
		t.Errorf("closed directory: %v", got)
	}
	if got := snapshot.Processes[701].Sockets; got != domain.Partial {
		t.Errorf("denied link: %v", got)
	}
	if got := snapshot.On(port8000)[0].Completeness(); got != domain.Partial {
		t.Errorf("socket: %v", got)
	}
}

// TestOwnerlessSocketIsUnavailable covers sockets with no visible holder.
func TestOwnerlessSocketIsUnavailable(t *testing.T) {
	fake := newFakeProc(t)
	fake.listen(8000, "0", "77")
	fake.listen(8001, "0", "0")
	fake.process(fakeProcess{pid: 600, ppid: 1, uid: "1000", start: 60, inodes: []string{"0"}})
	snapshot := scanFake(t, fake)
	for _, socket := range snapshot.Sockets {
		if len(socket.Holders) != 0 || socket.Completeness() != domain.Unavailable {
			t.Errorf("got %+v", socket)
		}
	}
}

// TestMalformedTableMarksThePort proves bad rows reach the port state.
func TestMalformedTableMarksThePort(t *testing.T) {
	fake := newFakeProc(t)
	fake.listen(8000, "1000", "77")
	fake.write("proc/net/tcp6", tableHeader+"garbage\n")
	if got := scanFake(t, fake).Ports[port8000].Table; got != domain.Partial {
		t.Errorf("got %v", got)
	}
}

// TestMissingTables allows no IPv6 but not a missing TCP table.
func TestMissingTables(t *testing.T) {
	fake := newFakeProc(t)
	for _, name := range []string{"tcp6", "udp", "udp6"} {
		if err := os.Remove(filepath.Join(fake.dir, "proc/net", name)); err != nil {
			t.Fatal(err)
		}
	}
	fake.listen(8000, "1000", "77")
	if got := scanFake(t, fake).Ports[port8000].Table; got != domain.Complete {
		t.Errorf("got %v", got)
	}
	if err := os.Remove(filepath.Join(fake.dir, "proc/net/tcp")); err != nil {
		t.Fatal(err)
	}
	if _, err := fake.source().scan(t.Context()); err == nil {
		t.Error("a missing TCP table must fail")
	}
}

// TestHiddenProcessesArePartial covers hidepid.
func TestHiddenProcessesArePartial(t *testing.T) {
	fake := newFakeProc(t)
	if err := os.RemoveAll(filepath.Join(fake.dir, "proc/1")); err != nil {
		t.Fatal(err)
	}
	if got := scanFake(t, fake).Host.ProcessTable; got != domain.Partial {
		t.Errorf("got %v", got)
	}
}

// TestExpiredScanIsPartial proves a timeout is never called complete.
func TestExpiredScanIsPartial(t *testing.T) {
	fake := newFakeProc(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	snapshot, err := fake.source().scan(ctx)
	if err != nil || snapshot.Host.ProcessTable != domain.Partial || len(snapshot.Warnings) != 1 {
		t.Errorf("got %+v, %v", snapshot.Host, err)
	}
}

// TestReplacedProcessIsDropped covers a PID reused during the scan.
func TestReplacedProcessIsDropped(t *testing.T) {
	fake := newFakeProc(t)
	fake.process(fakeProcess{pid: 600, ppid: 1, uid: "1000", start: 60})
	known := domain.Process{ProcessID: domain.ProcessID{PID: 600, Start: "59"}}
	if _, ok := fake.source().detail(known, ""); ok {
		t.Error("another start time is another process")
	}
}

// TestReread covers a held socket, another descriptor, and every change.
func TestReread(t *testing.T) {
	fake := newFakeProc(t)
	fake.process(fakeProcess{pid: 600, ppid: 1, uid: "1000", start: 60, inodes: []string{"77"}})
	source, id := fake.source(), domain.SocketID{OSID: "77"}
	process, holds, err := source.reread(domain.Holder{PID: 600, FD: 3}, id)
	if err != nil || !holds || !process.Same(domain.ProcessID{PID: 600, Start: "60", UserID: "1000", Exe: "/usr/bin/app"}) {
		t.Fatalf("got %+v %v %v", process.ProcessID, holds, err)
	}
	if _, holds, _ := source.reread(domain.Holder{PID: 600, FD: 99}, id); !holds {
		t.Error("a moved descriptor still holds the socket")
	}
	fake.link("proc/600/fd/3", "socket:[78]")
	if _, holds, _ := source.reread(domain.Holder{PID: 600, FD: 3}, id); holds {
		t.Error("the socket was replaced")
	}
	for _, unknown := range []string{"", "0"} {
		if _, holds, _ := source.reread(domain.Holder{PID: 600, FD: 3}, domain.SocketID{OSID: unknown}); holds {
			t.Errorf("unknown socket %q can't be held", unknown)
		}
	}
	if _, _, err := source.reread(domain.Holder{PID: 999, FD: 3}, id); err == nil {
		t.Error("a missing process must fail")
	}
}

// childVariable turns the test binary into a listener that ends by itself.
const childVariable = "PORTLY_DISCOVERY_CHILD"

// TestMain runs the tests, or listens until told to close.
func TestMain(m *testing.M) {
	if os.Getenv(childVariable) == "" {
		os.Exit(m.Run())
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(1)
	}
	fmt.Println(listener.Addr().(*net.TCPAddr).Port)
	time.AfterFunc(30*time.Second, func() { os.Exit(9) })
	input := bufio.NewReader(os.Stdin)
	_, _ = input.ReadString('\n')
	_ = listener.Close()
	fmt.Println("closed")
	_, _ = input.ReadString('\n')
}

// TestRealListener finds another process, because emulators fake the own one.
func TestRealListener(t *testing.T) {
	command := exec.Command(os.Args[0])
	command.Env = append(os.Environ(), childVariable+"=1")
	input, errIn := command.StdinPipe()
	output, errOut := command.StdoutPipe()
	if err := errors.Join(errIn, errOut, command.Start()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		_ = command.Wait()
	})
	lines := bufio.NewReader(output)
	line, _ := lines.ReadString('\n')
	port, err := strconv.ParseUint(strings.TrimSpace(line), 10, 16)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := domain.Endpoint{Port: uint16(port), Protocol: domain.TCP}
	snapshot, err := New().Observe(t.Context(), []domain.Endpoint{endpoint})
	if err != nil {
		t.Fatal(err)
	}
	pid, sockets := command.Process.Pid, snapshot.On(endpoint)
	if len(sockets) != 1 || !sockets[0].HeldBy(pid) || snapshot.Ports[endpoint].Stable != domain.Yes {
		t.Fatalf("got %+v, %+v", sockets, snapshot.Ports[endpoint])
	}
	process, holds, err := New().Reread(sockets[0].Holders[0], sockets[0].ID)
	if err != nil || !holds || !process.Same(snapshot.Processes[pid].ProcessID) {
		t.Errorf("reread %+v %v %v, want %+v", process.ProcessID, holds, err, snapshot.Processes[pid].ProcessID)
	}
	_, _ = input.Write([]byte("close\n"))
	_, _ = lines.ReadString('\n')
	if _, holds, _ := New().Reread(sockets[0].Holders[0], sockets[0].ID); holds {
		t.Error("a closed socket is not held")
	}
}

// TestRealUDP finds a bound UDP endpoint of this test process.
func TestRealUDP(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	endpoint := domain.Endpoint{Port: uint16(conn.LocalAddr().(*net.UDPAddr).Port), Protocol: domain.UDP}
	snapshot, err := New().Scan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if sockets := snapshot.On(endpoint); len(sockets) != 1 || !sockets[0].HeldBy(os.Getpid()) {
		t.Errorf("got %+v", sockets)
	}
}

// TestLargeScanStaysFast scans 5,000 fake processes under the 3 s budget.
func TestLargeScanStaysFast(t *testing.T) {
	fake := newFakeProc(t)
	for pid := 1000; pid < 6000; pid++ {
		dir := fmt.Sprintf("proc/%d/", pid)
		fake.write(dir+"stat", strconv.Itoa(pid)+" (app) S 1 1 1 0 0 0 0 0 0 0 0 0 0 0 20 0 1 0 9 0")
		fake.link(dir+"fd/0", "/dev/null")
	}
	started := time.Now()
	snapshot := scanFake(t, fake)
	if elapsed := time.Since(started); elapsed > 3*time.Second || len(snapshot.Processes) != 5002 {
		t.Errorf("%d processes in %v", len(snapshot.Processes), elapsed)
	}
}

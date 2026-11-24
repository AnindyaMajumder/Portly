package stopper

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// Every child ends by itself, even if its test crashes
const childLifetime = 20 * time.Second

// TestMain turns the test binary into a harmless child when asked.
func TestMain(m *testing.M) {
	mode := os.Getenv("PORTLY_TEST_CHILD")
	if mode == "" {
		os.Exit(m.Run())
	}
	if mode == "ignore" {
		signal.Ignore(syscall.SIGTERM)
	}
	_, _ = os.Stdout.WriteString("ready\n")
	time.Sleep(childLifetime)
}

// startChild spawns a child of this test and returns its identity.
func startChild(t *testing.T, mode string) (*exec.Cmd, domain.ProcessID) {
	t.Helper()
	command := exec.Command(os.Args[0])
	command.Env = append(os.Environ(), "PORTLY_TEST_CHILD="+mode)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	if _, err := output.Read(make([]byte, 6)); err != nil {
		t.Fatal(err)
	}
	return command, domain.ProcessID{PID: command.Process.Pid, Start: startOf(t, command.Process.Pid), UserID: strconv.Itoa(os.Getuid()), Exe: os.Args[0]}
}

// startOf reads a child's start time.
func startOf(t *testing.T, pid int) string {
	t.Helper()
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))[19]
}

// TestAliveWithoutPidfd tells zombies and reused PIDs apart.
func TestAliveWithoutPidfd(t *testing.T) {
	command, id := startChild(t, "normal")
	pinned := &processPin{id: id, fd: noPidfd}
	reused := &processPin{id: domain.ProcessID{PID: id.PID, Start: "1"}, fd: noPidfd}
	if !pinned.Alive() || reused.Alive() {
		t.Error("only the same process is alive")
	}
	_ = command.Process.Kill()
	for deadline := time.Now().Add(5 * time.Second); pinned.Alive(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a zombie is not alive")
		}
	}
}

// approve builds an approval for one child.
func approve(t *testing.T, id domain.ProcessID, method domain.Method, isSuspended bool) *domain.Approval {
	t.Helper()
	target := domain.StopTarget{
		Kind: domain.KindProcess, Root: id, Members: []domain.ProcessID{id}, Suspended: isSuspended,
		Named: []domain.SocketID{listener(web, "77").ID}, GracefulAvailable: true,
	}
	approval, err := domain.Approve(target, target, method)
	if err != nil {
		t.Fatal(err)
	}
	return approval
}

// pinChild pins a child, with or without a pidfd.
func pinChild(t *testing.T, id domain.ProcessID, usePidfd bool) pin {
	t.Helper()
	if !usePidfd {
		return &processPin{id: id, fd: noPidfd}
	}
	pinned, err := pinProcess(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pinned.Close)
	return pinned
}

// waitSignal returns the signal that ended a child.
func waitSignal(t *testing.T, command *exec.Cmd) syscall.Signal {
	t.Helper()
	_ = command.Wait()
	return command.ProcessState.Sys().(syscall.WaitStatus).Signal()
}

// TestTerminateEndsOnlyTheChild sends SIGTERM with and without a pidfd.
func TestTerminateEndsOnlyTheChild(t *testing.T) {
	for _, usePidfd := range []bool{true, false} {
		command, id := startChild(t, "normal")
		other, _ := startChild(t, "normal")
		pinned := pinChild(t, id, usePidfd)
		if !pinned.Alive() {
			t.Fatal("child should be alive")
		}
		if delivery := pinned.Terminate(approve(t, id, domain.Graceful, false)); !delivery.Accepted || delivery.Resumed || len(delivery.Killed) != 0 {
			t.Errorf("got %+v", delivery)
		}
		if got := waitSignal(t, command); got != syscall.SIGTERM {
			t.Errorf("ended by %v", got)
		}
		if pinned.Alive() || pinned.Terminate(approve(t, id, domain.Graceful, false)).Code != domain.CodeChanged {
			t.Error("an ended child is not alive")
		}
		if syscall.Kill(other.Process.Pid, 0) != nil {
			t.Error("another child was affected")
		}
	}
}

// TestZombieIsNotAlive proves the pin sees an exit before it is collected.
func TestZombieIsNotAlive(t *testing.T) {
	command, id := startChild(t, "normal")
	pinned := pinChild(t, id, true)
	pinned.Terminate(approve(t, id, domain.Graceful, false))
	for deadline := time.Now().Add(5 * time.Second); pinned.Alive(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("still alive")
		}
	}
	if syscall.Kill(id.PID, 0) != nil {
		t.Skip("already collected")
	}
	_ = command.Wait()
}

// TestForceStopKillsAChildThatIgnoresSIGTERM needs its own approval.
func TestForceStopKillsAChildThatIgnoresSIGTERM(t *testing.T) {
	command, id := startChild(t, "ignore")
	pinned := pinChild(t, id, true)
	pinned.Terminate(approve(t, id, domain.Graceful, false))
	time.Sleep(200 * time.Millisecond)
	if !pinned.Alive() {
		t.Fatal("the child should ignore SIGTERM")
	}
	if delivery := pinned.ForceStop(approve(t, id, domain.Forced, false)); !delivery.Accepted || len(delivery.Killed) != 1 {
		t.Errorf("got %+v", delivery)
	}
	if got := waitSignal(t, command); got != syscall.SIGKILL {
		t.Errorf("ended by %v", got)
	}
}

// TestSuspendedChildIsResumed sends SIGCONT after SIGTERM.
func TestSuspendedChildIsResumed(t *testing.T) {
	command, id := startChild(t, "normal")
	if err := command.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	var status unix.WaitStatus
	if _, err := unix.Wait4(id.PID, &status, unix.WUNTRACED, nil); err != nil || !status.Stopped() {
		t.Fatalf("not stopped: %v", err)
	}
	if delivery := pinChild(t, id, true).Terminate(approve(t, id, domain.Graceful, true)); !delivery.Resumed {
		t.Errorf("got %+v", delivery)
	}
	if got := waitSignal(t, command); got != syscall.SIGTERM {
		t.Errorf("ended by %v", got)
	}
}

// TestExecutorNeedsTheRightApproval covers every wrong approval.
func TestExecutorNeedsTheRightApproval(t *testing.T) {
	_, id := startChild(t, "normal")
	_, otherID := startChild(t, "normal")
	pinned := pinChild(t, id, true)
	used := approve(t, id, domain.Graceful, false)
	_, _ = used.Take(domain.Graceful)
	deliveries := map[string]domain.Delivery{
		"nil":             pinned.Terminate(nil),
		"zero":            pinned.Terminate(&domain.Approval{}),
		"used":            pinned.Terminate(used),
		"forced as plain": pinned.Terminate(approve(t, id, domain.Forced, false)),
		"plain as forced": pinned.ForceStop(approve(t, id, domain.Graceful, false)),
		"other target":    pinned.Terminate(approve(t, otherID, domain.Graceful, false)),
		"other member":    pinned.ForceStop(approve(t, otherID, domain.Forced, false)),
		"nil forced":      pinned.ForceStop(nil),
	}
	for name, delivery := range deliveries {
		if delivery.Accepted {
			t.Errorf("%s was accepted", name)
		}
	}
	if !pinned.Alive() {
		t.Error("a refused approval reached the child")
	}
}

// TestDangerousPIDsAreRefused covers groups, everything, PID 1 and gone processes.
func TestDangerousPIDsAreRefused(t *testing.T) {
	for _, pid := range []int{-1, 0, 1, -os.Getpid()} {
		if _, err := pinProcess(domain.ProcessID{PID: pid}); err == nil {
			t.Errorf("pinned %d", pid)
		}
		// Signal 0 proves the guard without sending anything
		if delivery := (&processPin{id: domain.ProcessID{PID: pid}, fd: noPidfd}).send(0); delivery.Accepted {
			t.Errorf("send reached %d", pid)
		}
	}
	command, id := startChild(t, "normal")
	_ = command.Process.Kill()
	_ = command.Wait()
	if _, err := pinProcess(id); err == nil {
		t.Error("pinned a process that is gone")
	}
}

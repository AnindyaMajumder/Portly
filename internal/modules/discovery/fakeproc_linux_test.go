package discovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// fakeProc is a /proc tree in a temporary directory.
type fakeProc struct {
	t   *testing.T
	dir string
}

// newFakeProc builds a tree with PID 1, Portly and empty socket tables.
func newFakeProc(t *testing.T) fakeProc {
	t.Helper()
	fake := fakeProc{t, t.TempDir()}
	fake.write("proc/sys/net/ipv4/ip_local_port_range", "32768 60999\n")
	fake.write("etc/login.defs", "UID_MIN 1000\n")
	fake.write("run/systemd/system/.keep", "")
	fake.link("proc/self/ns/net", "net:[1]")
	fake.write("proc/self/cgroup", "0::/user.slice/session-1.scope\n")
	fake.write("proc/self/mountinfo", "")
	for _, table := range tables {
		fake.write("proc/net/"+table.name, tableHeader)
	}
	fake.process(fakeProcess{pid: 1, uid: "0", start: 1, cgroup: "/init.scope"})
	fake.process(fakeProcess{pid: 500, ppid: 1, uid: "1000", start: 50})
	return fake
}

// source reads the fake tree as user 1000.
func (f fakeProc) source() source {
	return source{
		root: filepath.Join(f.dir, "proc"), etc: filepath.Join(f.dir, "etc"), run: filepath.Join(f.dir, "run"),
		self: 500, caller: 1000, users: &accounts{},
		mainPID: func(context.Context, domain.Unit, string) (int, bool) { return 0, false },
	}
}

// write creates a file and its directories.
func (f fakeProc) write(path, text string) {
	f.t.Helper()
	full := filepath.Join(f.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// link creates a dangling symlink like the ones in /proc.
func (f fakeProc) link(path, target string) {
	f.t.Helper()
	full := filepath.Join(f.dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		f.t.Fatal(err)
	}
	_ = os.Remove(full)
	if err := os.Symlink(target, full); err != nil {
		f.t.Fatal(err)
	}
}

// listen adds a TCP listener row on 127.0.0.1.
func (f fakeProc) listen(port uint16, uid, inode string) {
	f.t.Helper()
	path := filepath.Join(f.dir, "proc/net/tcp")
	data, err := os.ReadFile(path)
	if err != nil {
		f.t.Fatal(err)
	}
	line := tableLine(fmt.Sprintf("%s:%04X", nativeHex(0x7F000001), port), "0A", uid, inode)
	f.write("proc/net/tcp", string(data)+line)
}

// fakeProcess describes one process directory.
type fakeProcess struct {
	pid, ppid, start int
	uid, state       string
	cgroup           string
	inodes           []string
	noFDs            bool
}

// process writes one process directory.
func (f fakeProc) process(p fakeProcess) {
	f.t.Helper()
	dir := fmt.Sprintf("proc/%d/", p.pid)
	state, cgroup := p.state, p.cgroup
	if state == "" {
		state = "S"
	}
	if cgroup == "" {
		cgroup = "/user.slice/session-1.scope"
	}
	f.write(dir+"stat", fmt.Sprintf("%d (app) %s %d %d %d 34816 0 0 0 0 0 0 0 0 0 0 20 0 1 0 %d 0", p.pid, state, p.ppid, p.pid, p.pid, p.start))
	f.write(dir+"status", "TracerPid:\t0\nUid:\t"+strings.Repeat(p.uid+"\t", 4)+"\nSigBlk:\t0\nSigIgn:\t0\nSigCgt:\t4000\n")
	f.write(dir+"cgroup", "0::"+cgroup+"\n")
	f.write(dir+"cmdline", "app\x00--port\x008000\x00")
	f.link(dir+"exe", "/usr/bin/app")
	f.link(dir+"cwd", "/home/dev/app")
	if p.noFDs {
		return
	}
	f.link(dir+"fd/0", "/dev/null")
	for i, inode := range p.inodes {
		f.link(fmt.Sprintf("%sfd/%d", dir, i+3), "socket:["+inode+"]")
	}
}

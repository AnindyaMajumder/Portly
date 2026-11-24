package discovery

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// Process files are capped at this size, the socket tables are not
const maxProcFile = 1 << 20

const scanWorkers = 8

// source reads /proc, or a fake tree in tests.
type source struct {
	root    string
	etc     string
	run     string
	self    int
	caller  int
	users   *accounts
	mainPID mainPIDFunc
}

// newSource reads the real system.
func newSource() source {
	return source{
		root: "/proc", etc: "/etc", run: "/run", self: os.Getpid(), caller: os.Geteuid(),
		users: &accounts{}, mainPID: systemctlMainPID,
	}
}

// table is one kernel socket table.
type table struct {
	name     string
	protocol domain.Protocol
	family   uint8
}

var tables = []table{{"tcp", domain.TCP, 4}, {"tcp6", domain.TCP, 6}, {"udp", domain.UDP, 4}, {"udp6", domain.UDP, 6}}

// scan joins sockets, descriptors and process details into one snapshot.
func (s source) scan(ctx context.Context) (domain.Snapshot, error) {
	sockets, completeness, err := s.sockets()
	if err != nil {
		return domain.Snapshot{}, err
	}
	pids, err := s.pids()
	if err != nil {
		return domain.Snapshot{}, err
	}
	snapshot := domain.Snapshot{Host: s.host(), Sockets: sockets, Processes: map[int]domain.Process{}}
	holders := s.readProcesses(ctx, pids, &snapshot)
	attachHolders(&snapshot, holders, completeness)
	s.addDetails(ctx, &snapshot)
	snapshot.Ports = portStates(snapshot.Sockets, completeness)
	if ctx.Err() != nil {
		snapshot.Host.ProcessTable = domain.Partial
		snapshot.Warnings = append(snapshot.Warnings, "The scan timed out, results are partial")
	}
	return snapshot, nil
}

// sockets reads every table. IPv6 and UDP may be missing, TCP may not.
func (s source) sockets() ([]domain.Socket, map[domain.Protocol]domain.Completeness, error) {
	netNS, err := os.Readlink(filepath.Join(s.root, "self/ns/net"))
	if err != nil {
		netNS = ""
	}
	completeness := map[domain.Protocol]domain.Completeness{domain.TCP: domain.Complete, domain.UDP: domain.Complete}
	var sockets []domain.Socket
	for _, table := range tables {
		data, err := os.ReadFile(filepath.Join(s.root, "net", table.name))
		if err != nil && table.name == "tcp" {
			return nil, nil, fmt.Errorf("read socket table: %w", err)
		}
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			completeness[table.protocol] = domain.Unavailable
			continue
		}
		found, quality := parseSocketTable(data, table.protocol, table.family, netNS)
		sockets = append(sockets, found...)
		completeness[table.protocol] = min(completeness[table.protocol], quality)
	}
	return sockets, completeness, nil
}

// pids lists every visible process.
func (s source) pids() ([]int, error) {
	dir, err := os.Open(s.root)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	var pids []int
	for _, name := range names {
		if pid, err := strconv.Atoi(name); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	slices.Sort(pids)
	return pids, nil
}

// readProcesses fills parents and descriptor access, and returns holders by inode.
func (s source) readProcesses(ctx context.Context, pids []int, snapshot *domain.Snapshot) map[string][]domain.Holder {
	wanted := make(map[string]bool, len(snapshot.Sockets))
	for _, socket := range snapshot.Sockets {
		wanted[socket.ID.OSID] = socket.ID.OSID != "0"
	}
	var mu sync.Mutex
	holders := map[string][]domain.Holder{}
	s.eachPID(ctx, pids, func(pid int) {
		stat, err := parseStat(s.read(pid, "stat"))
		if err != nil {
			return
		}
		held, completeness := s.heldSockets(pid, wanted)
		process := processFrom(pid, stat)
		process.Sockets = completeness
		mu.Lock()
		defer mu.Unlock()
		snapshot.Processes[pid] = process
		for inode, list := range held {
			holders[inode] = append(holders[inode], list...)
		}
	})
	return holders
}

// eachPID visits processes in parallel until the deadline.
func (s source) eachPID(ctx context.Context, pids []int, visit func(pid int)) {
	queue := make(chan int, len(pids))
	for _, pid := range pids {
		queue <- pid
	}
	close(queue)
	var workers sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), scanWorkers) {
		workers.Go(func() {
			for pid := range queue {
				if ctx.Err() != nil {
					return
				}
				visit(pid)
			}
		})
	}
	workers.Wait()
}

// heldSockets lists wanted sockets and whether every descriptor was readable.
func (s source) heldSockets(pid int, wanted map[string]bool) (map[string][]domain.Holder, domain.Completeness) {
	path := s.path(pid, "fd")
	dir, err := os.Open(path)
	if err != nil {
		return nil, domain.Unavailable
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	completeness := domain.Complete
	if err != nil {
		completeness = domain.Partial
	}
	held := map[string][]domain.Holder{}
	for _, name := range names {
		link, err := os.Readlink(path + "/" + name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		fd, errNumber := strconv.Atoi(name)
		if err != nil || errNumber != nil {
			completeness = domain.Partial
			continue
		}
		if inode, ok := socketInode(link); ok && wanted[inode] {
			held[inode] = append(held[inode], domain.Holder{PID: pid, FD: fd})
		}
	}
	return held, completeness
}

// attachHolders links sockets to holders and records how far to trust that.
func attachHolders(snapshot *domain.Snapshot, holders map[string][]domain.Holder, tables map[domain.Protocol]domain.Completeness) {
	everyProcess := domain.Complete
	for _, process := range snapshot.Processes {
		if process.Sockets != domain.Complete {
			everyProcess = domain.Partial
		}
	}
	for i := range snapshot.Sockets {
		socket := &snapshot.Sockets[i]
		socket.Holders = holders[socket.ID.OSID]
		slices.SortFunc(socket.Holders, func(a, b domain.Holder) int {
			return cmp.Or(cmp.Compare(a.PID, b.PID), cmp.Compare(a.FD, b.FD))
		})
		ownership := everyProcess
		if len(socket.Holders) == 0 {
			ownership = domain.Unavailable
		}
		socket.Evidence = []domain.Evidence{
			{Source: "proc-net", Completeness: tables[socket.ID.Protocol]},
			{Source: "proc-fd", Completeness: ownership},
		}
	}
}

// portStates records table quality for every endpoint with a socket.
func portStates(sockets []domain.Socket, tables map[domain.Protocol]domain.Completeness) map[domain.Endpoint]domain.PortState {
	states := map[domain.Endpoint]domain.PortState{}
	for protocol, completeness := range tables {
		states[domain.Endpoint{Protocol: protocol}] = domain.PortState{Table: completeness}
	}
	for _, socket := range sockets {
		states[socket.ID.Endpoint] = domain.PortState{Table: tables[socket.ID.Protocol]}
	}
	return states
}

// addDetails reads holders, their ancestors and their descendants fully.
func (s source) addDetails(ctx context.Context, snapshot *domain.Snapshot) {
	relevant := map[int]bool{}
	mark := func(pid int) {
		relevant[pid] = true
		ancestors, _ := snapshot.Ancestors(pid)
		for _, ancestor := range ancestors {
			relevant[ancestor.PID] = true
		}
	}
	mark(s.self)
	holders := domain.HolderPIDs(snapshot.Sockets)
	for _, pid := range holders {
		mark(pid)
	}
	for pid := range descendantsOf(*snapshot, holders) {
		relevant[pid] = true
	}
	for pid := range relevant {
		known, found := snapshot.Processes[pid]
		if !found || ctx.Err() != nil {
			continue
		}
		detailed, ok := s.detail(known, snapshot.Host.SelfContainer)
		if !ok {
			delete(snapshot.Processes, pid)
			continue
		}
		snapshot.Processes[pid] = detailed
	}
}

// descendantsOf finds everything below the given processes in one pass.
func descendantsOf(snapshot domain.Snapshot, roots []int) map[int]bool {
	children := map[int][]int{}
	for _, process := range snapshot.Processes {
		children[process.PPID] = append(children[process.PPID], process.PID)
	}
	found := map[int]bool{}
	queue := slices.Clone(roots)
	for ; len(queue) > 0; queue = queue[1:] {
		for _, child := range children[queue[0]] {
			if !found[child] {
				found[child] = true
				queue = append(queue, child)
			}
		}
	}
	return found
}

// processFrom keeps what a stat line says.
func processFrom(pid int, stat procStat) domain.Process {
	return domain.Process{
		ProcessID: domain.ProcessID{PID: pid, Start: stat.start}, PPID: stat.ppid, SessionID: stat.session,
		Name: stat.name, Kernel: stat.kernel, TTY: domain.TriOf(stat.tty != 0),
		Zombie: stat.state == "Z" || stat.state == "X", Suspended: stat.state == "T",
	}
}

// detail reads one process fully, false when it is gone or was replaced.
func (s source) detail(known domain.Process, selfContainer string) (domain.Process, bool) {
	pid := known.PID
	status, group := parseStatus(s.read(pid, "status")), parseCgroup(s.read(pid, "cgroup"))
	arguments, exe, directory := splitArgs(s.read(pid, "cmdline")), s.link(pid, "exe"), s.link(pid, "cwd")
	stat, err := parseStat(s.read(pid, "stat"))
	if err != nil || stat.start != known.Start {
		return domain.Process{}, false
	}
	process := processFrom(pid, stat)
	owner := s.users.lookup(status.userID)
	process.Sockets, process.UserID, process.RealUID = known.Sockets, status.userID, status.realUID
	process.User, process.Home, process.Exe, process.Dir, process.Args = owner.name, owner.home, exe, directory, arguments
	process.Tracer, process.Caught, process.Ignored, process.Blocked = status.tracer, status.caught, status.ignored, status.blocked
	process.ContainerIDs, process.Unit = outsideSelf(group.containerIDs, selfContainer), group.unit
	process.UserManager = group.manager
	return process, true
}

// outsideSelf drops Portly's own container and anything around it.
func outsideSelf(ids []string, self string) []string {
	if self == "" {
		return ids
	}
	if i := slices.Index(ids, self); i >= 0 {
		return ids[i+1:]
	}
	return ids
}

// path names a process file.
func (s source) path(pid int, name string) string {
	return s.root + "/" + strconv.Itoa(pid) + "/" + name
}

// read returns a capped process file, nil when unreadable.
func (s source) read(pid int, name string) []byte {
	return readCapped(s.path(pid, name))
}

// readCapped returns at most maxProcFile bytes, nil when unreadable.
func readCapped(path string) []byte {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	data, _ := io.ReadAll(io.LimitReader(file, maxProcFile))
	return data
}

// link reads a process symlink without following it, empty when unreadable.
func (s source) link(pid int, name string) string {
	target, err := os.Readlink(s.path(pid, name))
	if err != nil {
		return ""
	}
	return target
}

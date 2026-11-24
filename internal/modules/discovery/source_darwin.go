package discovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/unix"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

const (
	toolDeadline = 2 * time.Second
	systemUIDMax = 499
	stateStopped = 4
	stateZombie  = 5
	flagTraced   = 0x800
	flagSystem   = 0x200
	stopSignal   = 1 << 14
)

var errGone = errors.New("process is gone")

// source reads macOS through sysctl, lsof and netstat.
type source struct {
	users *accounts
}

// newSource reads the real system.
func newSource() source { return source{users: &accounts{}} }

// tool runs an Apple program by absolute path, with a minimal environment.
func tool(ctx context.Context, path string, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, toolDeadline)
	defer cancel()
	command := exec.CommandContext(ctx, path, arguments...)
	command.Env = []string{"LC_ALL=C"}
	return command.Output()
}

// scan joins lsof with the kernel's own list and process details.
func (s source) scan(ctx context.Context) (domain.Snapshot, error) {
	table, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return domain.Snapshot{}, fmt.Errorf("list processes: %w", err)
	}
	sockets, tables, err := s.sockets(ctx)
	if err != nil {
		return domain.Snapshot{}, err
	}
	snapshot := domain.Snapshot{Host: s.host(), Sockets: sockets, Processes: map[int]domain.Process{}}
	for i := range table {
		found := s.processFrom(&table[i], snapshot.Host)
		snapshot.Processes[found.PID] = found
	}
	addEvidence(&snapshot, tables)
	s.addDetails(ctx, &snapshot)
	snapshot.Ports = portStates(snapshot.Sockets, tables)
	if ctx.Err() != nil {
		snapshot.Host.ProcessTable = domain.Partial
		snapshot.Warnings = append(snapshot.Warnings, "The scan timed out, results are partial")
	}
	return snapshot, nil
}

// sockets lists what lsof sees, plus what only netstat sees.
func (s source) sockets(ctx context.Context) ([]domain.Socket, map[domain.Protocol]domain.Completeness, error) {
	output, err := tool(ctx, "/usr/sbin/lsof", "-nP", "-iTCP", "-sTCP:LISTEN", "-iUDP", "-F0pufdtPnT")
	var exited *exec.ExitError
	// lsof exits 1 when nothing matches
	if err != nil && !errors.As(err, &exited) {
		return nil, nil, fmt.Errorf("run lsof: %w", err)
	}
	sockets := parseLsof(output)
	completeness := map[domain.Protocol]domain.Completeness{}
	for _, protocol := range []domain.Protocol{domain.TCP, domain.UDP} {
		output, err := tool(ctx, "/usr/sbin/netstat", "-anl", "-p", string(protocol))
		if err != nil {
			completeness[protocol] = domain.Partial
			continue
		}
		kernel, quality := parseNetstat(output, protocol)
		completeness[protocol] = quality
		sockets = addHidden(sockets, kernel)
	}
	return sockets, completeness, nil
}

// host reads facts about this machine and caller.
func (s source) host() domain.Host {
	first, errFirst := unix.SysctlUint32("net.inet.ip.portrange.first")
	last, errLast := unix.SysctlUint32("net.inet.ip.portrange.last")
	host := domain.Host{
		SelfPID: os.Getpid(), CallerUID: strconv.Itoa(os.Geteuid()), Privileged: os.Geteuid() == 0, SystemUIDMax: systemUIDMax,
		InContainer: domain.No, ServiceManager: domain.Yes, ProcessTable: domain.Complete,
	}
	if errFirst == nil && errLast == nil && first > 0 && first <= last && last <= 65535 {
		host.Ephemeral = domain.Range{First: uint16(first), Last: uint16(last)}
	}
	return host
}

// processFrom reads what the kernel tells about any process.
func (s source) processFrom(info *unix.KinfoProc, host domain.Host) domain.Process {
	pid := int(info.Proc.P_pid)
	uid, realUID := strconv.FormatUint(uint64(info.Eproc.Ucred.Uid), 10), strconv.FormatUint(uint64(info.Eproc.Pcred.P_ruid), 10)
	owner := s.users.lookup(uid)
	found := domain.Process{
		ProcessID: domain.ProcessID{PID: pid, Start: startOf(info), UserID: uid}, PPID: int(info.Eproc.Ppid), RealUID: realUID,
		User: owner.name, Home: owner.home, Name: unix.ByteSliceToString(info.Proc.P_comm[:]),
		Kernel: info.Proc.P_flag&flagSystem != 0, TTY: domain.TriOf(info.Eproc.Tdev != -1),
		Suspended: info.Proc.P_stat == stateStopped, Zombie: info.Proc.P_stat == stateZombie,
		Tracer: domain.TriOf(info.Proc.P_flag&flagTraced != 0), Caught: domain.TriOf(info.Proc.P_sigcatch&stopSignal != 0),
		Ignored: domain.TriOf(info.Proc.P_sigignore&stopSignal != 0), Blocked: domain.TriOf(info.Proc.P_sigmask&stopSignal != 0),
		Unit: domain.Unit{Kind: domain.UnitNone}, Service: domain.No,
	}
	if session, err := unix.Getsid(pid); err == nil {
		found.SessionID = session
	}
	// lsof reads every descriptor of the caller's own processes
	if host.Privileged || uid == host.CallerUID && realUID == host.CallerUID {
		found.Sockets = domain.Complete
	}
	return found
}

// startOf is the start time in microseconds.
func startOf(info *unix.KinfoProc) string {
	return fmt.Sprintf("%d.%06d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec)
}

// addEvidence records how far to trust each socket's owner.
func addEvidence(snapshot *domain.Snapshot, tables map[domain.Protocol]domain.Completeness) {
	for i := range snapshot.Sockets {
		socket := &snapshot.Sockets[i]
		ownership := domain.Partial
		if len(socket.Holders) == 0 {
			ownership = domain.Unavailable
		}
		socket.Evidence = []domain.Evidence{
			{Source: "netstat", Completeness: tables[socket.ID.Protocol]}, {Source: "lsof", Completeness: ownership},
		}
	}
}

// addDetails reads paths and arguments of holders, ancestors and Portly.
func (s source) addDetails(ctx context.Context, snapshot *domain.Snapshot) {
	relevant := map[int]bool{snapshot.Host.SelfPID: true}
	for _, pid := range append(domain.HolderPIDs(snapshot.Sockets), snapshot.Host.SelfPID) {
		relevant[pid] = true
		ancestors, _ := snapshot.Ancestors(pid)
		for _, ancestor := range ancestors {
			relevant[ancestor.PID] = true
		}
	}
	var list []string
	for pid := range relevant {
		list = append(list, strconv.Itoa(pid))
	}
	output, _ := tool(ctx, "/bin/ps", "-ww", "-o", "pid=,args=", "-p", strings.Join(list, ","))
	arguments := parsePSArgs(output)
	for pid := range relevant {
		found, known := snapshot.Processes[pid]
		if !known || ctx.Err() != nil {
			continue
		}
		details := &process.Process{Pid: int32(pid)}
		found.Exe, _ = details.ExeWithContext(ctx)
		found.Dir, _ = details.CwdWithContext(ctx)
		found.Args = arguments[pid]
		snapshot.Processes[pid] = found
	}
}

// addManagers asks launchd about owners of named endpoints and their ancestors.
func (s source) addManagers(ctx context.Context, snapshot *domain.Snapshot, endpoints []domain.Endpoint) {
	for _, endpoint := range endpoints {
		for _, pid := range domain.HolderPIDs(snapshot.On(endpoint)) {
			found, known := snapshot.Processes[pid]
			if !known {
				continue
			}
			found.Job = s.job(ctx, *snapshot, found)
			snapshot.Processes[pid] = found
		}
	}
}

// job finds the nearest process that is a job's main process. Unreadable domains stay unknown.
func (s source) job(ctx context.Context, snapshot domain.Snapshot, owner domain.Process) domain.Job {
	labels := map[int]string{}
	for _, domainName := range []string{"system", "user/" + owner.UserID, "gui/" + owner.UserID} {
		output, err := tool(ctx, "/bin/launchctl", "print", domainName)
		found, isReadable := parseLaunchctl(output)
		if err != nil || !isReadable {
			return domain.Job{}
		}
		for pid, label := range found {
			labels[pid] = label
		}
	}
	ancestors, _ := snapshot.Ancestors(owner.PID)
	for _, candidate := range append([]domain.Process{owner}, ancestors...) {
		label, isMain := labels[candidate.PID]
		isApplication := strings.HasPrefix(label, "application.")
		if isMain && candidate.PID > 1 && (!isApplication || candidate.PID == owner.PID) {
			return domain.Job{Label: label, MainPID: candidate.PID, Application: isApplication, Known: true}
		}
	}
	return domain.Job{Known: true}
}

// reread finds the socket first and reads identity last, right before the signal.
func (s source) reread(holder domain.Holder, id domain.SocketID) (domain.Process, bool, error) {
	output, _ := tool(context.Background(), "/usr/sbin/lsof", "-nP", "-a", "-p", strconv.Itoa(holder.PID), "-i"+strings.ToUpper(string(id.Protocol))+":"+strconv.Itoa(int(id.Port)), "-F0pufdtPnT")
	holds := false
	for _, socket := range parseLsof(output) {
		holds = holds || socket.ID.Same(id) && socket.HeldBy(holder.PID)
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", holder.PID)
	if err != nil {
		return domain.Process{}, false, fmt.Errorf("read process %d: %w", holder.PID, errGone)
	}
	found := s.processFrom(info, s.host())
	found.Exe, _ = (&process.Process{Pid: int32(holder.PID)}).Exe()
	return found, holds, nil
}

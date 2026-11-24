package discovery

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// host reads facts about this machine and caller.
func (s source) host() domain.Host {
	own := parseCgroup(readCapped(filepath.Join(s.root, "self/cgroup")))
	host := domain.Host{
		SelfPID: s.self, CallerUID: strconv.Itoa(s.caller), Privileged: s.caller == 0,
		SystemUIDMax:   parseSystemUIDMax(readCapped(filepath.Join(s.etc, "login.defs"))),
		Ephemeral:      parseRange(readCapped(filepath.Join(s.root, "sys/net/ipv4/ip_local_port_range"))),
		ServiceManager: domain.TriOf(exists(filepath.Join(s.run, "systemd/system"))),
		ProcessTable:   domain.Complete,
	}
	if len(own.containerIDs) > 0 {
		host.SelfContainer = own.containerIDs[len(own.containerIDs)-1]
	} else {
		host.SelfContainer = parseContainerMount(readCapped(filepath.Join(s.root, "self/mountinfo")))
	}
	host.InContainer = s.inContainer(host, own)
	// hidepid hides other users, so descendants could be missing
	if _, err := parseStat(s.read(1, "stat")); err != nil {
		host.ProcessTable = domain.Partial
	}
	return host
}

// inContainer says whether Portly runs in a container, unknown when unclear.
func (s source) inContainer(host domain.Host, own cgroupInfo) domain.Tri {
	hasMarker := exists(filepath.Join(s.run, ".containerenv")) || exists(filepath.Join(s.etc, "../.dockerenv"))
	switch {
	case host.SelfContainer != "" || hasMarker:
		return domain.Yes
	case host.ServiceManager == domain.Yes || own.unit.Kind >= domain.UnitService || s.isInitialNamespace():
		return domain.No
	}
	return domain.Unknown
}

// Fixed inodes of the kernel's first namespaces
const (
	initialPIDNamespace    = "pid:[4026531836]"
	initialCgroupNamespace = "cgroup:[4026531835]"
)

// isInitialNamespace proves this is no container, also without systemd.
func (s source) isInitialNamespace() bool {
	pid, errPID := os.Readlink(filepath.Join(s.root, "self/ns/pid"))
	cgroup, errCgroup := os.Readlink(filepath.Join(s.root, "self/ns/cgroup"))
	return errPID == nil && errCgroup == nil && pid == initialPIDNamespace && cgroup == initialCgroupNamespace
}

// exists reports whether a path is there.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

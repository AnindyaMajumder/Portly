package discovery

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// Kernel states: 0A is LISTEN, 07 an unconnected UDP socket
const (
	tcpListen      = "0A"
	udpUnconnected = "07"
	udpConnected   = "01"
)

var errMalformed = errors.New("malformed kernel data")

// parseSocketTable reads /proc/net/{tcp,udp}{,6}. Bad lines make it partial.
func parseSocketTable(data []byte, protocol domain.Protocol, family uint8, netNS string) ([]domain.Socket, domain.Completeness) {
	completeness := domain.Complete
	var sockets []domain.Socket
	header := true
	for line := range strings.Lines(string(data)) {
		if header {
			header = false
			continue
		}
		socket, wanted, err := parseSocketLine(line, protocol, family)
		if err != nil {
			completeness = domain.Partial
			continue
		}
		if wanted {
			socket.ID.NetNS = netNS
			sockets = append(sockets, socket)
		}
	}
	if header {
		return nil, domain.Unavailable
	}
	return sockets, completeness
}

// parseSocketLine reads one row, false when Portly does not need it.
func parseSocketLine(line string, protocol domain.Protocol, family uint8) (domain.Socket, bool, error) {
	fields := strings.Fields(line)
	if len(fields) < 10 {
		return domain.Socket{}, false, errMalformed
	}
	state := fields[3]
	isWanted := protocol == domain.TCP && state == tcpListen ||
		protocol == domain.UDP && (state == udpUnconnected || state == udpConnected)
	address, port, err := parseHexEndpoint(fields[1])
	if err != nil || address.Is4() != (family == 4) {
		return domain.Socket{}, false, errMalformed
	}
	_, errUser := strconv.ParseUint(fields[7], 10, 32)
	_, errInode := strconv.ParseUint(fields[9], 10, 64)
	if errUser != nil || errInode != nil {
		return domain.Socket{}, false, errMalformed
	}
	if !isWanted || port == 0 {
		return domain.Socket{}, false, nil
	}
	endpoint := domain.Endpoint{Port: port, Protocol: protocol}
	return domain.Socket{
		ID: domain.SocketID{
			AddressPort: domain.AddressPort{Endpoint: endpoint, Addr: address}, Family: family, OSID: fields[9],
		},
		CreatorUID: fields[7], Connected: protocol == domain.UDP && state == udpConnected,
	}, true, nil
}

// parseHexEndpoint decodes kernel hex, which is host byte order per word.
func parseHexEndpoint(text string) (netip.Addr, uint16, error) {
	host, portText, found := strings.Cut(text, ":")
	raw, err := hex.DecodeString(host)
	if !found || err != nil || (len(raw) != 4 && len(raw) != 16) {
		return netip.Addr{}, 0, errMalformed
	}
	port, err := strconv.ParseUint(portText, 16, 16)
	if err != nil {
		return netip.Addr{}, 0, errMalformed
	}
	for i := 0; i < len(raw); i += 4 {
		binary.NativeEndian.PutUint32(raw[i:], binary.BigEndian.Uint32(raw[i:]))
	}
	address, _ := netip.AddrFromSlice(raw)
	return address, uint16(port), nil
}

// procStat is the part of /proc/PID/stat Portly needs.
type procStat struct {
	name    string
	state   string
	ppid    int
	session int
	tty     int
	kernel  bool
	start   string
}

const kernelThreadFlag = 0x00200000

// parseStat handles names containing spaces and parentheses.
func parseStat(data []byte) (procStat, error) {
	open, closing := bytes.IndexByte(data, '('), bytes.LastIndexByte(data, ')')
	if open < 0 || closing < open {
		return procStat{}, errMalformed
	}
	fields := strings.Fields(string(data[closing+1:]))
	if len(fields) < 20 {
		return procStat{}, errMalformed
	}
	ppid, errParent := strconv.Atoi(fields[1])
	session, errSession := strconv.Atoi(fields[3])
	tty, errTTY := strconv.Atoi(fields[4])
	flags, errFlags := strconv.ParseUint(fields[6], 10, 64)
	_, errStart := strconv.ParseUint(fields[19], 10, 64)
	if errors.Join(errParent, errSession, errTTY, errFlags, errStart) != nil || ppid < 0 {
		return procStat{}, errMalformed
	}
	return procStat{
		name: string(data[open+1 : closing]), state: fields[0], ppid: ppid, session: session, tty: tty,
		kernel: flags&kernelThreadFlag != 0, start: fields[19],
	}, nil
}

// procStatus is the part of /proc/PID/status Portly needs.
type procStatus struct {
	realUID string
	userID  string
	tracer  domain.Tri
	caught  domain.Tri
	ignored domain.Tri
	blocked domain.Tri
}

// SIGTERM is signal 15, so bit 14
const stopSignalBit = 1 << 14

// parseStatus reads users, tracer and SIGTERM handling.
func parseStatus(data []byte) procStatus {
	var status procStatus
	for line := range strings.Lines(string(data)) {
		key, value, _ := strings.Cut(line, ":")
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		switch key {
		case "Uid":
			if len(fields) == 4 {
				status.realUID, status.userID = fields[0], fields[1]
			}
		case "TracerPid":
			if tracer, err := strconv.Atoi(fields[0]); err == nil {
				status.tracer = domain.TriOf(tracer != 0)
			}
		case "SigCgt":
			status.caught = stopSignalIn(fields[0])
		case "SigIgn":
			status.ignored = stopSignalIn(fields[0])
		case "SigBlk":
			status.blocked = stopSignalIn(fields[0])
		}
	}
	return status
}

// stopSignalIn reports whether a signal mask contains SIGTERM.
func stopSignalIn(mask string) domain.Tri {
	bits, err := strconv.ParseUint(mask, 16, 64)
	if err != nil {
		return domain.Unknown
	}
	return domain.TriOf(bits&stopSignalBit != 0)
}

// Podman's conmon has its own scope and is not the container
var containerSegment = regexp.MustCompile(`^(?:docker-|libpod-|cri-containerd-|crio-)?([0-9a-f]{64})(?:\.scope)?$`)

// cgroupInfo says which containers and unit a process belongs to.
type cgroupInfo struct {
	containerIDs []string
	// Path up to each container, in the same order
	containerPaths []string
	unit           domain.Unit
	manager        bool
}

// parseCgroup reads containers and the service unit, for cgroup v1 and v2.
func parseCgroup(data []byte) cgroupInfo {
	var info, other cgroupInfo
	for line := range strings.Lines(string(data)) {
		parts := strings.SplitN(strings.TrimSpace(line), ":", 3)
		if len(parts) != 3 || !strings.HasPrefix(parts[2], "/") {
			continue
		}
		ids, paths := containersIn(parts[2])
		if parts[0] != "0" && parts[1] != "name=systemd" {
			if len(ids) > len(other.containerIDs) {
				other.containerIDs, other.containerPaths = ids, paths
			}
			continue
		}
		if len(ids) > len(info.containerIDs) {
			info.containerIDs, info.containerPaths = ids, paths
		}
		if info.unit.Kind < domain.UnitService {
			info.unit, info.manager = innermostUnit(parts[2])
		}
	}
	if len(info.containerIDs) == 0 {
		info.containerIDs, info.containerPaths = other.containerIDs, other.containerPaths
	}
	return info
}

// containersIn lists container IDs in a cgroup path and the path up to each, outermost first.
func containersIn(path string) ([]string, []string) {
	var ids, paths, walked []string
	for part := range strings.SplitSeq(path, "/") {
		walked = append(walked, part)
		if match := containerSegment.FindStringSubmatch(part); match != nil {
			ids, paths = append(ids, match[1]), append(paths, strings.Join(walked, "/"))
		}
	}
	return ids, paths
}

// outside drops Portly's own container and anything around it.
func (c cgroupInfo) outside(self string) ([]string, string) {
	// Not found gives -1, so everything stays
	first := slices.Index(c.containerIDs, self) + 1
	if first == len(c.containerIDs) {
		return nil, ""
	}
	return c.containerIDs[first:], c.containerPaths[first]
}

// innermostUnit finds the last service or scope in a cgroup path.
func innermostUnit(path string) (domain.Unit, bool) {
	unit := domain.Unit{Kind: domain.UnitNone}
	isUser := false
	for part := range strings.SplitSeq(path, "/") {
		switch {
		case strings.HasPrefix(part, "user@") && strings.HasSuffix(part, ".service"):
			unit = domain.Unit{Name: part, Kind: domain.UnitService}
			isUser = true
		case strings.HasSuffix(part, ".service"):
			unit = domain.Unit{Name: part, Kind: domain.UnitService, User: isUser}
		case strings.HasSuffix(part, ".scope"):
			unit = domain.Unit{Name: part, Kind: domain.UnitScope, User: isUser}
		}
	}
	return unit, unit.Name == "init.scope"
}

// splitArgs parses NUL-separated arguments.
func splitArgs(data []byte) []string {
	text := strings.TrimRight(string(data), "\x00")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\x00")
}

// socketInode extracts N from socket:[N].
func socketInode(link string) (string, bool) {
	inner, found := strings.CutPrefix(link, "socket:[")
	if !found {
		return "", false
	}
	return strings.CutSuffix(inner, "]")
}

// parseRange reads two ports, zero when malformed.
func parseRange(data []byte) domain.Range {
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return domain.Range{}
	}
	first, errFirst := strconv.ParseUint(fields[0], 10, 16)
	last, errLast := strconv.ParseUint(fields[1], 10, 16)
	if errFirst != nil || errLast != nil || first > last {
		return domain.Range{}
	}
	return domain.Range{First: uint16(first), Last: uint16(last)}
}

// Distributions without login.defs still start users at 1000
const defaultSystemUIDMax = 999

// parseSystemUIDMax reads UID_MIN from login.defs.
func parseSystemUIDMax(data []byte) uint32 {
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "UID_MIN" {
			continue
		}
		if first, err := strconv.ParseUint(fields[1], 10, 32); err == nil && first > 0 {
			return max(uint32(first-1), defaultSystemUIDMax)
		}
	}
	return defaultSystemUIDMax
}

var containerMount = regexp.MustCompile(`/(?:containers|overlay-containers)/([0-9a-f]{64})/`)

// parseContainerMount finds our container ID when the cgroup hides it.
func parseContainerMount(mountInfo []byte) string {
	for line := range strings.Lines(string(mountInfo)) {
		fields := strings.Fields(line)
		// Engine hosts show container paths too, but not bound over /etc
		if len(fields) < 5 || !strings.HasPrefix(fields[4], "/etc/host") {
			continue
		}
		if match := containerMount.FindStringSubmatch(fields[3]); match != nil {
			return match[1]
		}
	}
	return ""
}

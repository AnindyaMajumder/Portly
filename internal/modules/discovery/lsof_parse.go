package discovery

import (
	"net/netip"
	"strconv"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// macOS has no network namespaces
const singleNamespace = "host"

// lsofFile is one file record of lsof -F0 output.
type lsofFile struct {
	pid    int
	userID string
	fields map[byte]string
}

// parseLsof reads NUL-separated lsof -F0 records and joins shared sockets.
func parseLsof(data []byte) []domain.Socket {
	index := map[domain.SocketID]int{}
	var sockets []domain.Socket
	for _, file := range lsofFiles(data) {
		socket, ok := file.socket()
		if !ok {
			continue
		}
		at, isSeen := index[socket.ID]
		if !isSeen || socket.ID.OSID == "" {
			index[socket.ID] = len(sockets)
			sockets = append(sockets, socket)
			continue
		}
		sockets[at].Holders = append(sockets[at].Holders, socket.Holders...)
	}
	return sockets
}

// lsofFiles splits process and file records.
func lsofFiles(data []byte) []lsofFile {
	var files []lsofFile
	var current lsofFile
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := map[byte]string{}
		for field := range strings.SplitSeq(line, "\x00") {
			// TCP info repeats, only the state matters
			if field == "" || field[0] == 'T' && !strings.HasPrefix(field, "TST=") {
				continue
			}
			fields[field[0]] = field[1:]
		}
		if pid, isProcess := fields['p']; isProcess {
			current = lsofFile{userID: fields['u']}
			current.pid, _ = strconv.Atoi(pid)
			continue
		}
		if _, isFile := fields['f']; isFile && current.pid > 0 {
			files = append(files, lsofFile{pid: current.pid, userID: current.userID, fields: fields})
		}
	}
	return files
}

// socket turns a listening TCP or bound UDP record into a socket.
func (f lsofFile) socket() (domain.Socket, bool) {
	protocol := domain.Protocol(strings.ToLower(f.fields['P']))
	isListener := protocol == domain.TCP && f.fields['T'] == "ST=LISTEN" || protocol == domain.UDP
	isIPv6 := f.fields['t'] == "IPv6"
	local, _, isConnected := strings.Cut(f.fields['n'], "->")
	address, port, ok := parseLsofName(local, isIPv6)
	fd, err := strconv.Atoi(f.fields['f'])
	if !isListener || !ok || err != nil {
		return domain.Socket{}, false
	}
	id := domain.SocketID{
		AddressPort: domain.AddressPort{Endpoint: domain.Endpoint{Port: port, Protocol: protocol}, Addr: address.WithZone(""), Scope: address.Zone()},
		Family:      familyOf(isIPv6), NetNS: singleNamespace, OSID: kernelAddress(f.fields['d']),
	}
	return domain.Socket{ID: id, CreatorUID: f.userID, Connected: isConnected, Holders: []domain.Holder{{PID: f.pid, FD: fd}}}, true
}

// kernelAddress keeps a real socket address. Anything else stays unknown.
func kernelAddress(device string) string {
	if !strings.HasPrefix(device, "0x") || strings.Trim(device, "0x") == "" {
		return ""
	}
	return device
}

// familyOf is 4 or 6.
func familyOf(isIPv6 bool) uint8 {
	if isIPv6 {
		return 6
	}
	return 4
}

// parseLsofName accepts *:80, 127.0.0.1:80 and [::1]:80.
func parseLsofName(name string, isIPv6 bool) (netip.Addr, uint16, bool) {
	colon := strings.LastIndexByte(name, ':')
	if colon < 0 {
		return netip.Addr{}, 0, false
	}
	return hostAndPort(strings.Trim(name[:colon], "[]"), name[colon+1:], isIPv6)
}

// hostAndPort reads an address, where * means any.
func hostAndPort(host, portText string, isIPv6 bool) (netip.Addr, uint16, bool) {
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return netip.Addr{}, 0, false
	}
	if host == "*" && isIPv6 {
		return netip.IPv6Unspecified(), uint16(port), true
	}
	if host == "*" {
		return netip.IPv4Unspecified(), uint16(port), true
	}
	address, err := netip.ParseAddr(host)
	return address, uint16(port), err == nil
}

// parseNetstat reads macOS netstat -anl, which sees every user's sockets.
func parseNetstat(data []byte, protocol domain.Protocol) ([]domain.Socket, domain.Completeness) {
	completeness := domain.Complete
	var sockets []domain.Socket
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.HasPrefix(fields[0], string(protocol)) {
			continue
		}
		isListener := protocol == domain.UDP || len(fields) > 5 && fields[5] == "LISTEN"
		dot := strings.LastIndexByte(fields[3], '.')
		if !isListener || dot < 0 || fields[3][dot+1:] == "*" {
			continue
		}
		isIPv6 := strings.Contains(fields[0], "6")
		address, port, ok := hostAndPort(fields[3][:dot], fields[3][dot+1:], isIPv6)
		if !ok {
			completeness = domain.Partial
			continue
		}
		socket := domain.Socket{Connected: protocol == domain.UDP && fields[4] != "*.*"}
		socket.ID = domain.SocketID{
			AddressPort: domain.AddressPort{Endpoint: domain.Endpoint{Port: port, Protocol: protocol}, Addr: address.WithZone(""), Scope: address.Zone()},
			Family:      familyOf(isIPv6), NetNS: singleNamespace,
		}
		sockets = append(sockets, socket)
	}
	return sockets, completeness
}

// addHidden appends kernel sockets that lsof could not attribute.
func addHidden(visible, kernel []domain.Socket) []domain.Socket {
	unseen := map[domain.AddressPort]int{}
	for _, socket := range visible {
		unseen[socket.ID.AddressPort]++
	}
	for _, socket := range kernel {
		if unseen[socket.ID.AddressPort]--; unseen[socket.ID.AddressPort] < 0 {
			visible = append(visible, socket)
		}
	}
	return visible
}

// parseLaunchctl maps main PIDs to labels, from the services block of launchctl print.
func parseLaunchctl(data []byte) (map[int]string, bool) {
	labels := map[int]string{}
	isInside, isFound := false, false
	for line := range strings.Lines(string(data)) {
		text := strings.TrimSpace(line)
		switch {
		case text == "services = {":
			isInside, isFound = true, true
		case text == "}":
			isInside = false
		case isInside:
			fields := strings.Fields(text)
			if len(fields) < 3 {
				continue
			}
			if pid, err := strconv.Atoi(fields[0]); err == nil && pid > 0 {
				labels[pid] = fields[len(fields)-1]
			}
		}
	}
	return labels, isFound
}

// parsePSArgs reads ps -o pid=,args= output. Spaces inside arguments are lost.
func parsePSArgs(data []byte) map[int][]string {
	found := map[int][]string{}
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if pid, err := strconv.Atoi(fields[0]); err == nil {
			found[pid] = fields[1:]
		}
	}
	return found
}

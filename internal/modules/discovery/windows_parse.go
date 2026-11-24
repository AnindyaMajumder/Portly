package discovery

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strconv"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// rowLayout gives byte offsets in an iphlpapi OWNER_MODULE row.
type rowLayout struct {
	size, address, addressLen, scope, port, pid, created int
	protocol                                             domain.Protocol
	family                                               uint8
}

// Offsets follow 8-byte alignment on every Windows architecture
var (
	tcp4Row = rowLayout{160, 4, 4, -1, 8, 20, 24, domain.TCP, 4}
	tcp6Row = rowLayout{192, 0, 16, 16, 20, 52, 56, domain.TCP, 6}
	udp4Row = rowLayout{160, 0, 4, -1, 4, 8, 16, domain.UDP, 4}
	udp6Row = rowLayout{176, 0, 16, 16, 20, 24, 32, domain.UDP, 6}
)

// The row count, padded to the row alignment
const winTableHeader = 8

// Creation this close to the bind can't be ordered safely, in 100 ns
const bindTolerance = 10_000

// winSocket pairs a socket with its recorded owner and bind time.
type winSocket struct {
	domain.Socket
	pid     int
	created uint64
}

// parseWindowsTable decodes a table without trusting its row count.
func parseWindowsTable(table []byte, layout rowLayout) ([]winSocket, domain.Completeness) {
	if len(table) < winTableHeader {
		return nil, domain.Unavailable
	}
	var sockets []winSocket
	rows := table[winTableHeader:]
	for count := binary.LittleEndian.Uint32(table); count > 0; count-- {
		if len(rows) < layout.size {
			return sockets, domain.Partial
		}
		sockets = append(sockets, layout.socket(rows[:layout.size]))
		rows = rows[layout.size:]
	}
	return sockets, domain.Complete
}

// socket decodes one row. Without a bind time the identity stays unknown.
func (l rowLayout) socket(row []byte) winSocket {
	address, _ := netip.AddrFromSlice(row[l.address : l.address+l.addressLen])
	found := winSocket{pid: int(binary.LittleEndian.Uint32(row[l.pid:])), created: binary.LittleEndian.Uint64(row[l.created:])}
	found.ID = domain.SocketID{
		// Ports are stored in network byte order
		AddressPort: domain.AddressPort{Endpoint: domain.Endpoint{Port: binary.BigEndian.Uint16(row[l.port:]), Protocol: l.protocol}, Addr: address},
		Family:      l.family, NetNS: singleNamespace,
	}
	if l.scope >= 0 {
		if scope := binary.LittleEndian.Uint32(row[l.scope:]); scope != 0 {
			found.ID.Scope = strconv.FormatUint(uint64(scope), 10)
		}
	}
	if found.created != 0 && found.pid != 0 {
		found.ID.OSID = fmt.Sprintf("%d-%d", found.pid, found.created)
	}
	return found
}

// ownsSince requires an owner clearly older than its socket, so a reused PID never matches.
func ownsSince(processStart, bound uint64) bool {
	return processStart != 0 && bound != 0 && processStart+bindTolerance < bound
}

package discovery

import (
	"slices"
	"strings"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// lsofRecord joins fields the way lsof -F0 does.
func lsofRecord(fields ...string) string { return strings.Join(fields, "\x00") + "\x00\n" }

// TestParseLsof covers listeners, shared sockets, UDP and unknown IDs.
func TestParseLsof(t *testing.T) {
	data := lsofRecord("p10", "u501") +
		lsofRecord("f3", "tIPv4", "d0xabc", "PTCP", "n127.0.0.1:8000", "TST=LISTEN", "TQR=0") +
		lsofRecord("f4", "tIPv4", "d0xdef", "PTCP", "n127.0.0.1:8000->127.0.0.1:5", "TST=ESTABLISHED") +
		lsofRecord("f5", "tIPv6", "d0x123", "PUDP", "n*:5353") +
		lsofRecord("f6", "tIPv4", "d0x0", "PUDP", "n10.0.0.1:53->8.8.8.8:53") +
		lsofRecord("p11", "u501") +
		lsofRecord("f3", "tIPv4", "d0xabc", "PTCP", "n127.0.0.1:8000", "TST=LISTEN")
	sockets := parseLsof([]byte(data))
	if len(sockets) != 3 {
		t.Fatalf("got %+v", sockets)
	}
	shared := sockets[0]
	if !slices.Equal(shared.Holders, []domain.Holder{{PID: 10, FD: 3}, {PID: 11, FD: 3}}) || shared.CreatorUID != "501" || shared.ID.OSID != "0xabc" {
		t.Errorf("shared: %+v", shared)
	}
	if sockets[1].ID.String() != "[::]:5353" || sockets[1].ID.Family != 6 || sockets[1].Connected {
		t.Errorf("udp: %+v", sockets[1])
	}
	if !sockets[2].Connected || sockets[2].ID.OSID != "" || sockets[2].ID.Same(sockets[2].ID) {
		t.Errorf("an invented id must never match: %+v", sockets[2])
	}
}

// TestParseLsofRejects covers malformed names and records.
func TestParseLsofRejects(t *testing.T) {
	for _, name := range []string{"", "x", "*:0", "*:99999", "host:80", "*:http"} {
		data := lsofRecord("p10", "u1") + lsofRecord("f3", "tIPv4", "d0x1", "PTCP", "n"+name, "TST=LISTEN")
		if got := parseLsof([]byte(data)); len(got) != 0 {
			t.Errorf("%q: %+v", name, got)
		}
	}
	if got := parseLsof([]byte(lsofRecord("f3", "tIPv4", "PTCP", "n*:80", "TST=LISTEN"))); len(got) != 0 {
		t.Errorf("a file without a process: %+v", got)
	}
	if got := parseLsof([]byte(lsofRecord("p1") + lsofRecord("fcwd", "PTCP", "n*:80", "TST=LISTEN"))); len(got) != 0 {
		t.Errorf("not a descriptor: %+v", got)
	}
}

// TestHiddenSockets adds what only the kernel list shows.
func TestHiddenSockets(t *testing.T) {
	kernel, completeness := parseNetstat([]byte("Active Internet connections\nProto Recv-Q Send-Q  Local Address          Foreign Address        (state)\n"+
		"tcp4       0      0  127.0.0.1.8000         *.*                    LISTEN\n"+
		"tcp4       0      0  *.22                   *.*                    LISTEN\n"+
		"tcp46      0      0  *.9000                 *.*                    LISTEN\n"+
		"tcp4       0      0  10.0.0.1.5000          1.1.1.1.443            ESTABLISHED\n"), domain.TCP)
	if completeness != domain.Complete || len(kernel) != 3 || kernel[2].ID.Family != 6 {
		t.Fatalf("got %+v %v", kernel, completeness)
	}
	visible := parseLsof([]byte(lsofRecord("p10", "u501") + lsofRecord("f3", "tIPv4", "d0xabc", "PTCP", "n127.0.0.1:8000", "TST=LISTEN")))
	joined := addHidden(visible, kernel)
	if len(joined) != 3 || len(joined[1].Holders) != 0 || joined[1].ID.Port != 22 || joined[1].CreatorUID != "" {
		t.Errorf("got %+v", joined)
	}
	udp, _ := parseNetstat([]byte("udp4 0 0 *.5353 *.*\nudp4 0 0 10.0.0.1.53 8.8.8.8.53\nudp4 0 0 *.* *.*\n"), domain.UDP)
	if len(udp) != 2 || udp[0].Connected || !udp[1].Connected {
		t.Errorf("udp: %+v", udp)
	}
	if _, completeness := parseNetstat([]byte("tcp4 0 0 nonsense.80 *.* LISTEN\n"), domain.TCP); completeness != domain.Partial {
		t.Errorf("malformed: %v", completeness)
	}
}

// TestParseLaunchctl reads only the services block.
func TestParseLaunchctl(t *testing.T) {
	data := "system = {\n\tpid = 5\n\tservices = {\n\t\t     123      - \tcom.apple.x\n\t\t       0      - \tcom.idle\n\t\t     77     (pe) \thomebrew.mxcl.redis\n\t}\n\tother = {\n\t\t 9 - no\n\t}\n}\n"
	labels, ok := parseLaunchctl([]byte(data))
	if !ok || len(labels) != 2 || labels[123] != "com.apple.x" || labels[77] != "homebrew.mxcl.redis" {
		t.Errorf("got %v %v", labels, ok)
	}
	if _, ok := parseLaunchctl([]byte("Could not find domain\n")); ok {
		t.Error("an unreadable domain must be unknown")
	}
}

// TestParsePSArgs keeps arguments per process.
func TestParsePSArgs(t *testing.T) {
	got := parsePSArgs([]byte("  12 node server.js\n 13 x\nbad line\n 14\n"))
	if len(got) != 2 || !slices.Equal(got[12], []string{"node", "server.js"}) {
		t.Errorf("got %v", got)
	}
}

// FuzzMacParsers proves no macOS parser panics or invents a port.
func FuzzMacParsers(f *testing.F) {
	f.Add([]byte(lsofRecord("p10", "u501") + lsofRecord("f3", "tIPv4", "d0xabc", "PTCP", "n*:80", "TST=LISTEN")))
	f.Add([]byte("tcp4 0 0 *.22 *.* LISTEN\n"))
	f.Add([]byte("services = {\n 1 - x\n}\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		sockets := parseLsof(data)
		kernel, _ := parseNetstat(data, domain.UDP)
		for _, socket := range addHidden(sockets, kernel) {
			if socket.ID.Port == 0 {
				t.Fatal("port 0")
			}
		}
		parseLaunchctl(data)
		parsePSArgs(data)
	})
}

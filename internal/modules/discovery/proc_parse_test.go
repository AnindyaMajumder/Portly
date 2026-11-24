package discovery

import (
	"encoding/binary"
	"slices"
	"strings"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

const tableHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

// tableLine renders one socket table row.
func tableLine(local, state, uid, inode string) string {
	return "   0: " + local + " 00000000:0000 " + state + " 00000000:00000000 00:00000000 00000000 " + uid + " 0 " + inode + " 1 0000 100 0\n"
}

// nativeHex renders an address the way this CPU's kernel prints it.
func nativeHex(words ...uint32) string {
	var text strings.Builder
	for _, word := range words {
		raw := binary.BigEndian.AppendUint32(nil, word)
		const digits = "0123456789ABCDEF"
		ordered := binary.NativeEndian.Uint32(raw)
		for shift := 28; shift >= 0; shift -= 4 {
			text.WriteByte(digits[ordered>>shift&0xF])
		}
	}
	return text.String()
}

// TestParseSocketTable keeps listeners and skips everything else.
func TestParseSocketTable(t *testing.T) {
	loopback := nativeHex(0x7F000001)
	data := tableHeader +
		tableLine(loopback+":1F40", "0A", "1000", "111") +
		tableLine(loopback+":1F41", "01", "1000", "112") +
		tableLine(loopback+":0000", "0A", "1000", "113")
	sockets, completeness := parseSocketTable([]byte(data), domain.TCP, 4, "ns")
	if completeness != domain.Complete || len(sockets) != 1 {
		t.Fatalf("got %d sockets, %v", len(sockets), completeness)
	}
	got := sockets[0]
	if got.ID.String() != "127.0.0.1:8000" || got.ID.OSID != "111" || got.CreatorUID != "1000" || got.ID.NetNS != "ns" || got.ID.Family != 4 {
		t.Errorf("got %+v", got)
	}
}

// TestParseConnectedUDPIsKept proves policy can see connected UDP.
func TestParseConnectedUDPIsKept(t *testing.T) {
	address := nativeHex(0x0A000001)
	data := tableHeader + tableLine(address+":0035", "07", "0", "1") + tableLine(address+":0035", "01", "0", "2")
	sockets, _ := parseSocketTable([]byte(data), domain.UDP, 4, "ns")
	if len(sockets) != 2 || sockets[0].Connected || !sockets[1].Connected {
		t.Errorf("got %+v", sockets)
	}
}

// TestParseIPv6Table checks IPv6 word order.
func TestParseIPv6Table(t *testing.T) {
	data := tableHeader + tableLine(nativeHex(0, 0, 0, 1)+":0050", "0A", "0", "5")
	sockets, completeness := parseSocketTable([]byte(data), domain.TCP, 6, "ns")
	if completeness != domain.Complete || len(sockets) != 1 || sockets[0].ID.String() != "[::1]:80" {
		t.Errorf("got %+v %v", sockets, completeness)
	}
}

// TestMalformedTableIsNotComplete proves bad data is never called complete.
func TestMalformedTableIsNotComplete(t *testing.T) {
	good := tableLine(nativeHex(0x7F000001)+":1F40", "0A", "1000", "111")
	cases := map[string]struct {
		data string
		want domain.Completeness
	}{
		"empty":         {"", domain.Unavailable},
		"short line":    {tableHeader + good + "garbage\n", domain.Partial},
		"bad address":   {tableHeader + tableLine("XYZ:1F40", "0A", "0", "1"), domain.Partial},
		"wrong family":  {tableHeader + tableLine(nativeHex(0, 0, 0, 1)+":1F40", "0A", "0", "1"), domain.Partial},
		"bad port":      {tableHeader + tableLine("0100007F:1F40F", "0A", "0", "1"), domain.Partial},
		"bad inode":     {tableHeader + tableLine("0100007F:1F40", "0A", "0", "x"), domain.Partial},
		"bad user":      {tableHeader + tableLine("0100007F:1F40", "0A", "-1", "1"), domain.Partial},
		"only a header": {tableHeader, domain.Complete},
	}
	for name, c := range cases {
		if _, got := parseSocketTable([]byte(c.data), domain.TCP, 4, "ns"); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}

// TestParseStat covers hostile process names and malformed rows.
func TestParseStat(t *testing.T) {
	stat, err := parseStat([]byte("42 (a) (b c) T 7 42 40 34816 42 4194304 1 2 3 4 5 6 7 8 20 0 1 0 98765 1 1"))
	if err != nil {
		t.Fatal(err)
	}
	want := procStat{name: "a) (b c", state: "T", ppid: 7, session: 40, tty: 34816, start: "98765"}
	if stat != want {
		t.Errorf("got %+v, want %+v", stat, want)
	}
	for _, bad := range []string{"", "42 (x", "42 )x(", "42 (x) S 1", "42 (x) S a 1 1 0 1 0 1 2 3 4 5 6 7 8 20 0 1 0 9"} {
		if _, err := parseStat([]byte(bad)); err == nil {
			t.Errorf("%q should fail", bad)
		}
	}
}

// TestParseStatus proves unreadable values stay unknown.
func TestParseStatus(t *testing.T) {
	status := parseStatus([]byte("Name:\tx\nTracerPid:\t9\nUid:\t1000\t1001\t1000\t1000\nSigBlk:\t0000000000004000\nSigIgn:\t0000000000000000\nSigCgt:\t0000000000004002\n"))
	want := procStatus{realUID: "1000", userID: "1001", tracer: domain.Yes, caught: domain.Yes, ignored: domain.No, blocked: domain.Yes}
	if status != want {
		t.Errorf("got %+v, want %+v", status, want)
	}
	if got := parseStatus([]byte("SigCgt:\tzz\nUid:\t1\n")); got != (procStatus{}) {
		t.Errorf("unreadable values must stay unknown, got %+v", got)
	}
}

// TestParseCgroup covers units, containers, managers and namespaces.
func TestParseCgroup(t *testing.T) {
	id, inner := strings.Repeat("a", 64), strings.Repeat("b", 64)
	cases := []struct {
		name, data string
		ids        []string
		unit       string
		kind       domain.UnitKind
		user, mgr  bool
	}{
		{"terminal scope", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/app-x.slice/vte-1.scope\n", nil, "vte-1.scope", domain.UnitScope, true, false},
		{"system service", "0::/system.slice/ssh.service\n", nil, "ssh.service", domain.UnitService, false, false},
		{"user service", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/web.service\n", nil, "web.service", domain.UnitService, true, false},
		{"delegated subgroup", "0::/system.slice/web.service/workers\n", nil, "web.service", domain.UnitService, false, false},
		{"user manager", "0::/user.slice/user-1000.slice/user@1000.service/init.scope\n", nil, "init.scope", domain.UnitScope, true, true},
		{"pid 1", "0::/init.scope\n", nil, "init.scope", domain.UnitScope, false, true},
		{"docker systemd", "0::/system.slice/docker-" + id + ".scope\n", []string{id}, "docker-" + id + ".scope", domain.UnitScope, false, false},
		{"docker cgroupfs v1", "5:cpu:/docker/" + id + "\n1:name=systemd:/docker/" + id + "\n", []string{id}, "", domain.UnitNone, false, false},
		{"nested", "0::/docker/" + id + "/docker/" + inner + "\n", []string{id, inner}, "", domain.UnitNone, false, false},
		{"podman", "0::/user.slice/user@1000.service/user.slice/libpod-" + id + ".scope/container\n", []string{id}, "libpod-" + id + ".scope", domain.UnitScope, true, false},
		{"podman conmon", "0::/user.slice/libpod-conmon-" + id + ".scope\n", nil, "libpod-conmon-" + id + ".scope", domain.UnitScope, false, false},
		{"private namespace", "0::/\n", nil, "", domain.UnitNone, false, false},
		{"unreadable", "", nil, "", domain.UnitUnknown, false, false},
		{"relative path", "0::..\n", nil, "", domain.UnitUnknown, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseCgroup([]byte(c.data))
			if !slices.Equal(got.containerIDs, c.ids) || got.unit.Name != c.unit || got.unit.Kind != c.kind || got.unit.User != c.user || got.manager != c.mgr {
				t.Errorf("got %+v", got)
			}
		})
	}
}

// TestSmallParsers covers ranges, user limits, mounts, arguments and links.
func TestSmallParsers(t *testing.T) {
	if got := parseRange([]byte("32768\t60999\n")); got != (domain.Range{First: 32768, Last: 60999}) {
		t.Errorf("range: %v", got)
	}
	for _, bad := range []string{"", "5", "9 5", "a b", "1 70000"} {
		if got := parseRange([]byte(bad)); got != (domain.Range{}) {
			t.Errorf("range %q: %v", bad, got)
		}
	}
	for text, want := range map[string]uint32{"UID_MIN 1000\n": 999, "#UID_MIN 5\nUID_MIN\t\t2000\n": 1999, "UID_MIN 500\n": 999, "": 999, "UID_MIN x": 999} {
		if got := parseSystemUIDMax([]byte(text)); got != want {
			t.Errorf("uid max %q: %d", text, got)
		}
	}
	id := strings.Repeat("c", 64)
	if got := parseContainerMount([]byte("1 2 0:1 /var/lib/docker/containers/" + id + "/hostname /etc/hostname rw")); got != id {
		t.Errorf("mount id: %q", got)
	}
	if got := splitArgs([]byte("a\x00\x00b\x00")); !slices.Equal(got, []string{"a", "", "b"}) {
		t.Errorf("args: %q", got)
	}
	if inode, ok := socketInode("socket:[12]"); !ok || inode != "12" {
		t.Errorf("inode: %q %v", inode, ok)
	}
	for _, link := range []string{"pipe:[12]", "socket:12", "/tmp/socket:[12]"} {
		if _, ok := socketInode(link); ok {
			t.Errorf("%q is not a socket", link)
		}
	}
}

// FuzzProcParsers proves no parser panics or returns an invalid socket.
func FuzzProcParsers(f *testing.F) {
	f.Add([]byte(tableHeader + tableLine("0100007F:1F40", "0A", "1000", "111")))
	f.Add([]byte("42 (a) (b c) T 7 42 40 34816 42 4194304 1 2 3 4 5 6 7 8 20 0 1 0 98765"))
	f.Add([]byte("0::/system.slice/ssh.service\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, family := range []uint8{4, 6} {
			sockets, _ := parseSocketTable(data, domain.UDP, family, "ns")
			for _, socket := range sockets {
				if socket.ID.Port == 0 || !socket.ID.Addr.IsValid() {
					t.Fatalf("invalid socket %+v", socket)
				}
			}
		}
		if stat, err := parseStat(data); err == nil && stat.ppid < 0 {
			t.Fatal("negative parent")
		}
		parseStatus(data)
		parseCgroup(data)
		parseRange(data)
		parseSystemUIDMax(data)
		parseContainerMount(data)
		splitArgs(data)
	})
}

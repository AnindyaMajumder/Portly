package discovery

import (
	"encoding/binary"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// windowsRow builds one row of a table.
func windowsRow(layout rowLayout, address []byte, port uint16, pid uint32, created uint64) []byte {
	row := make([]byte, layout.size)
	copy(row[layout.address:], address)
	binary.BigEndian.PutUint16(row[layout.port:], port)
	binary.LittleEndian.PutUint32(row[layout.pid:], pid)
	binary.LittleEndian.PutUint64(row[layout.created:], created)
	return row
}

// windowsTable prefixes rows with a count.
func windowsTable(count uint32, rows ...[]byte) []byte {
	table := binary.LittleEndian.AppendUint32(nil, count)
	table = append(table, 0, 0, 0, 0)
	for _, row := range rows {
		table = append(table, row...)
	}
	return table
}

// TestParseWindowsTables covers every layout.
func TestParseWindowsTables(t *testing.T) {
	loopback6 := make([]byte, 16)
	loopback6[15] = 1
	cases := []struct {
		layout  rowLayout
		address []byte
		want    string
	}{
		{tcp4Row, []byte{127, 0, 0, 1}, "127.0.0.1:8000"}, {tcp6Row, loopback6, "[::1]:8000"},
		{udp4Row, []byte{0, 0, 0, 0}, "0.0.0.0:8000"}, {udp6Row, loopback6, "[::1]:8000"},
	}
	for _, c := range cases {
		sockets, completeness := parseWindowsTable(windowsTable(1, windowsRow(c.layout, c.address, 8000, 42, 999)), c.layout)
		if completeness != domain.Complete || len(sockets) != 1 {
			t.Fatalf("%v: %+v %v", c.layout, sockets, completeness)
		}
		got := sockets[0]
		if got.ID.String() != c.want || got.pid != 42 || got.ID.OSID != "42-999" || got.ID.Family != c.layout.family || got.ID.Protocol != c.layout.protocol {
			t.Errorf("got %+v", got)
		}
	}
}

// TestWindowsScope keeps link-local addresses apart.
func TestWindowsScope(t *testing.T) {
	row := windowsRow(tcp6Row, make([]byte, 16), 80, 1, 1)
	binary.LittleEndian.PutUint32(row[tcp6Row.scope:], 7)
	sockets, _ := parseWindowsTable(windowsTable(1, row), tcp6Row)
	if sockets[0].ID.Scope != "7" {
		t.Errorf("got %+v", sockets[0].ID)
	}
}

// TestMissingBindTimeIsUnknown never matches itself.
func TestMissingBindTimeIsUnknown(t *testing.T) {
	for _, row := range [][]byte{windowsRow(tcp4Row, []byte{127, 0, 0, 1}, 80, 42, 0), windowsRow(tcp4Row, []byte{127, 0, 0, 1}, 80, 0, 5)} {
		sockets, _ := parseWindowsTable(windowsTable(1, row), tcp4Row)
		if sockets[0].ID.Same(sockets[0].ID) {
			t.Errorf("got %+v", sockets[0].ID)
		}
	}
}

// TestLyingRowCountIsPartial covers truncated tables.
func TestLyingRowCountIsPartial(t *testing.T) {
	row := windowsRow(tcp4Row, []byte{127, 0, 0, 1}, 80, 42, 5)
	if sockets, got := parseWindowsTable(windowsTable(4000000000, row), tcp4Row); got != domain.Partial || len(sockets) != 1 {
		t.Errorf("%d sockets, %v", len(sockets), got)
	}
	if _, got := parseWindowsTable([]byte{1}, tcp4Row); got != domain.Unavailable {
		t.Errorf("got %v", got)
	}
}

// TestReusedPIDNeverOwns covers younger, equal, close and unknown times.
func TestReusedPIDNeverOwns(t *testing.T) {
	cases := []struct {
		start, bound uint64
		want         bool
	}{
		{1000, 1000 + bindTolerance + 1, true}, {1000, 1000 + bindTolerance, false}, {1000, 1000, false}, {2000, 1000, false}, {0, 1000, false}, {1000, 0, false},
	}
	for _, c := range cases {
		if got := ownsSince(c.start, c.bound); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

// FuzzWindowsTable proves no table panics.
func FuzzWindowsTable(f *testing.F) {
	f.Add(windowsTable(1, windowsRow(udp6Row, make([]byte, 16), 80, 1, 1)))
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, layout := range []rowLayout{tcp4Row, tcp6Row, udp4Row, udp6Row} {
			parseWindowsTable(data, layout)
		}
	})
}

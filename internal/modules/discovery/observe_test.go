package discovery

import (
	"context"
	"errors"
	"testing"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

var named = domain.Endpoint{Port: 8000, Protocol: domain.TCP}

// view is a snapshot with one socket held by one process.
func view(inode string, pid int, start string) domain.Snapshot {
	socket := domain.Socket{Holders: []domain.Holder{{PID: pid, FD: 3}}}
	socket.ID.Endpoint, socket.ID.OSID = named, inode
	return domain.Snapshot{
		Sockets:   []domain.Socket{socket},
		Processes: map[int]domain.Process{pid: {ProcessID: domain.ProcessID{PID: pid, Start: start}}},
	}
}

// script replays snapshots, one per scan.
func script(views ...domain.Snapshot) (scanFunc, *int) {
	calls := 0
	return func(context.Context) (domain.Snapshot, error) {
		calls++
		return views[min(calls, len(views))-1], nil
	}, &calls
}

// TestObserveStability covers stable, settling and never-settling endpoints.
func TestObserveStability(t *testing.T) {
	a, b, c := view("1", 10, "5"), view("2", 11, "6"), view("3", 12, "7")
	reused := view("1", 10, "9")
	cases := []struct {
		name  string
		views []domain.Snapshot
		want  domain.Tri
		calls int
	}{
		{"stable", []domain.Snapshot{a, a}, domain.Yes, 2},
		{"settles", []domain.Snapshot{a, b, b}, domain.Yes, 3},
		{"keeps changing", []domain.Snapshot{a, b, c}, domain.No, 3},
		{"pid reused", []domain.Snapshot{a, reused, a}, domain.No, 3},
		{"closed", []domain.Snapshot{a, {}, {}}, domain.Yes, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scan, calls := script(c.views...)
			got, err := observe(t.Context(), scan, []domain.Endpoint{named})
			if err != nil || got.Ports[named].Stable != c.want || *calls != c.calls {
				t.Errorf("stable %v after %d scans, %v", got.Ports[named].Stable, *calls, err)
			}
		})
	}
}

// TestObserveKeepsLatestView proves the decision uses the newest scan.
func TestObserveKeepsLatestView(t *testing.T) {
	scan, _ := script(view("1", 10, "5"), view("2", 11, "6"), view("2", 11, "6"))
	got, _ := observe(t.Context(), scan, []domain.Endpoint{named})
	if got.Sockets[0].ID.OSID != "2" {
		t.Errorf("got %+v", got.Sockets[0])
	}
}

// TestObserveFailsWithTheScan passes scan errors on.
func TestObserveFailsWithTheScan(t *testing.T) {
	calls := 0
	for failAt := 1; failAt <= 2; failAt++ {
		calls = 0
		scan := func(context.Context) (domain.Snapshot, error) {
			if calls++; calls == failAt {
				return domain.Snapshot{}, errors.New("broken")
			}
			return domain.Snapshot{}, nil
		}
		if _, err := observe(t.Context(), scan, []domain.Endpoint{named}); err == nil {
			t.Errorf("scan %d failed silently", failAt)
		}
	}
}

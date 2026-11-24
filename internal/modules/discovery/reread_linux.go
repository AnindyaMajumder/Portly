package discovery

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

var errGone = errors.New("process is gone")

// reread finds the socket first and reads identity last, right before the signal.
func (s source) reread(holder domain.Holder, id domain.SocketID) (domain.Process, bool, error) {
	holds := s.stillHolds(holder, id)
	stat, err := parseStat(s.read(holder.PID, "stat"))
	if err != nil {
		return domain.Process{}, false, fmt.Errorf("read process %d: %w", holder.PID, errGone)
	}
	process := processFrom(holder.PID, stat)
	status := parseStatus(s.read(holder.PID, "status"))
	process.UserID, process.RealUID = status.userID, status.realUID
	process.Tracer, process.Caught, process.Ignored, process.Blocked = status.tracer, status.caught, status.ignored, status.blocked
	process.Exe = s.link(holder.PID, "exe")
	return process, holds, nil
}

// stillHolds checks the recorded descriptor first, then the others.
func (s source) stillHolds(holder domain.Holder, id domain.SocketID) bool {
	if id.OSID == "" || id.OSID == "0" {
		return false
	}
	recorded, err := os.Readlink(s.path(holder.PID, "fd/"+strconv.Itoa(holder.FD)))
	if inode, ok := socketInode(recorded); err == nil && ok && inode == id.OSID {
		return true
	}
	held, _ := s.heldSockets(holder.PID, map[string]bool{id.OSID: true})
	return len(held[id.OSID]) > 0
}

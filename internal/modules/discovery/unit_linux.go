package discovery

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

const (
	unitDeadline  = 1 * time.Second
	maxUnitOutput = 64
)

// The user owns their cgroup directory, so unit names are hostile
var unitName = regexp.MustCompile(`^[A-Za-z0-9:_.@\\][A-Za-z0-9:_.@\\-]{0,254}$`)

// mainPIDFunc asks the service manager for a unit's main process.
type mainPIDFunc func(ctx context.Context, unit domain.Unit, callerUID string) (int, bool)

// addManagers asks who the main process is, for owners of named endpoints only.
func (s source) addManagers(ctx context.Context, snapshot *domain.Snapshot, endpoints []domain.Endpoint) {
	asked := map[domain.Unit]domain.Unit{}
	for _, endpoint := range endpoints {
		for _, pid := range domain.HolderPIDs(snapshot.On(endpoint)) {
			process, known := snapshot.Processes[pid]
			if !known || process.Unit.Kind != domain.UnitService {
				continue
			}
			unit, done := asked[process.Unit]
			if !done {
				unit = s.withMainPID(ctx, process, snapshot.Host.CallerUID)
				asked[process.Unit] = unit
			}
			process.Unit = unit
			snapshot.Processes[pid] = process
		}
	}
}

// withMainPID leaves the main process unknown when it can't be asked safely.
func (s source) withMainPID(ctx context.Context, process domain.Process, callerUID string) domain.Unit {
	unit := process.Unit
	isOthers := unit.User && process.UserID != callerUID
	if isOthers || !unitName.MatchString(unit.Name) {
		return unit
	}
	unit.MainPID, unit.MainKnown = s.mainPID(ctx, unit, callerUID)
	return unit
}

// systemctlMainPID only reads, and never sees names that look like options.
func systemctlMainPID(ctx context.Context, unit domain.Unit, callerUID string) (int, bool) {
	ctx, cancel := context.WithTimeout(ctx, unitDeadline)
	defer cancel()
	arguments := []string{"show", "--property=MainPID", "--value", "--", unit.Name}
	environment := []string{"LC_ALL=C"}
	if unit.User {
		arguments = append([]string{"--user"}, arguments...)
		environment = append(environment, "XDG_RUNTIME_DIR=/run/user/"+callerUID)
	}
	command := exec.CommandContext(ctx, "/usr/bin/systemctl", arguments...)
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		return 0, false
	}
	return parseMainPID(output)
}

// parseMainPID treats 0, which systemd prints for unknown units, as unknown.
func parseMainPID(output []byte) (int, bool) {
	if len(output) > maxUnitOutput {
		return 0, false
	}
	pid, err := strconv.Atoi(string(bytes.TrimSpace(output)))
	return pid, err == nil && pid > 0
}

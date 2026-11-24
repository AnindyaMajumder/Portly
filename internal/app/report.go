package app

import (
	"context"
	"os/user"
	"slices"
	"strings"
	"sync"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// Report is what list and inspect found.
type Report struct {
	Ports     []domain.PortReport
	HiddenUDP int
	// Shown always, the result is incomplete
	Warnings []string
	// Shown with --verbose
	Notes []string
}

// List reports every accessible port. Likely-client UDP is hidden unless all.
func (a *App) List(ctx context.Context, all bool) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, scanDeadline)
	defer cancel()
	snapshot, notes, err := a.world.list(ctx)
	if err != nil {
		return Report{}, err
	}
	report := Report{Warnings: snapshot.Warnings, Notes: notes}
	for _, endpoint := range visibleEndpoints(snapshot) {
		isNoise := endpoint.Protocol == domain.UDP && snapshot.Host.Ephemeral.Contains(endpoint.Port)
		if isNoise && !all {
			report.HiddenUDP++
			continue
		}
		report.Ports = append(report.Ports, describe(snapshot, endpoint))
	}
	a.addProjects(ctx, snapshot, report.Ports)
	return report, nil
}

// Inspect reports named ports. A named port is never hidden.
func (a *App) Inspect(ctx context.Context, refs []domain.PortRef) (Report, error) {
	ctx, cancel := context.WithTimeout(ctx, scanDeadline)
	defer cancel()
	snapshot, notes, err := a.world.list(ctx)
	if err != nil {
		return Report{}, err
	}
	report := Report{Warnings: snapshot.Warnings, Notes: notes}
	for _, ref := range distinctRefs(refs) {
		report.Ports = append(report.Ports, inspectRef(snapshot, ref)...)
	}
	a.addProjects(ctx, snapshot, report.Ports)
	return report, nil
}

// distinctRefs drops repeats and keeps order.
func distinctRefs(refs []domain.PortRef) []domain.PortRef {
	var kept []domain.PortRef
	for _, ref := range refs {
		if !slices.Contains(kept, ref) {
			kept = append(kept, ref)
		}
	}
	return kept
}

// inspectRef covers TCP and UDP, and free ports.
func inspectRef(snapshot domain.Snapshot, ref domain.PortRef) []domain.PortReport {
	var reports []domain.PortReport
	for _, protocol := range []domain.Protocol{domain.TCP, domain.UDP} {
		report := describe(snapshot, domain.Endpoint{Port: ref.Port, Protocol: protocol})
		if ref.Matches(ref.Port, protocol) && report.Status != domain.NotListening {
			reports = append(reports, report)
		}
	}
	if len(reports) == 0 {
		return []domain.PortReport{{Ref: ref, Status: domain.NotListening}}
	}
	return reports
}

// visibleEndpoints lists ports in order, without connected UDP.
func visibleEndpoints(snapshot domain.Snapshot) []domain.Endpoint {
	var endpoints []domain.Endpoint
	for _, socket := range snapshot.Sockets {
		if !socket.Connected {
			endpoints = append(endpoints, socket.ID.Endpoint)
		}
	}
	for _, engine := range snapshot.Engines {
		for _, container := range engine.Containers {
			for _, binding := range container.Ports {
				endpoints = append(endpoints, binding.Endpoint())
			}
		}
	}
	slices.SortFunc(endpoints, domain.Endpoint.Compare)
	return slices.Compact(endpoints)
}

// describe finds every owner of one endpoint, for display only.
func describe(snapshot domain.Snapshot, endpoint domain.Endpoint) domain.PortReport {
	report := domain.PortReport{Ref: domain.PortRef{Port: endpoint.Port, Protocol: endpoint.Protocol}, Status: domain.Listening}
	sockets := snapshot.On(endpoint)
	owners, rest := containerOwners(snapshot, endpoint, sockets)
	owners = append(owners, nativeOwners(snapshot, rest)...)
	report.Owners, report.Ambiguous = owners, len(owners) > 1
	isInaccessible := !slices.ContainsFunc(owners, func(owner domain.Owner) bool { return owner.Access == domain.AccessOK })
	switch {
	case len(owners) == 0:
		report.Status = domain.NotListening
	case len(sockets) == 0:
		report.Status = domain.Published
	case isInaccessible:
		report.Status = domain.Inaccessible
	}
	return report
}

// containerOwners takes the sockets containers publish or hold, and returns the rest.
func containerOwners(snapshot domain.Snapshot, endpoint domain.Endpoint, sockets []domain.Socket) ([]domain.Owner, []domain.Socket) {
	var owners []domain.Owner
	rest := slices.Clone(sockets)
	for _, engine := range snapshot.Engines {
		for _, container := range engine.Containers {
			bindings := container.Publishes(endpoint)
			var mine []domain.Socket
			rest = slices.DeleteFunc(rest, func(socket domain.Socket) bool {
				isMine := publishes(bindings, socket) || holdsFrom(snapshot, socket, container.ID)
				if isMine {
					mine = append(mine, socket)
				}
				return isMine
			})
			if len(mine) > 0 || len(bindings) > 0 {
				owners = append(owners, containerOwner(snapshot, engine, container, mine))
			}
		}
	}
	return owners, rest
}

// containerOwner describes a container and its Compose project.
func containerOwner(snapshot domain.Snapshot, engine domain.Engine, container domain.Container, sockets []domain.Socket) domain.Owner {
	owner := domain.Owner{Type: "Docker", Access: domain.AccessOK, Container: &container, Listeners: sockets}
	if engine.Kind == "podman" {
		owner.Type = "Podman"
	}
	init, known := snapshot.Processes[container.InitPID]
	owner.Proven = known && container.ID != "" && slices.Contains(init.ContainerIDs, container.ID)
	if container.ComposeProject != "" {
		owner.Project = &domain.Project{
			Name: container.ComposeProject, Root: container.Directory, Confidence: domain.High, Evidence: "compose label",
		}
	}
	return owner
}

// publishes matches a socket with a container's bindings.
func publishes(bindings []domain.Binding, socket domain.Socket) bool {
	return slices.ContainsFunc(bindings, func(binding domain.Binding) bool {
		return binding.HostAddr == socket.ID.Addr || !binding.HostAddr.IsValid() && socket.ID.Addr.IsUnspecified()
	})
}

// holdsFrom reports whether a holder runs inside a container.
func holdsFrom(snapshot domain.Snapshot, socket domain.Socket, id string) bool {
	return slices.ContainsFunc(socket.Holders, func(holder domain.Holder) bool {
		return id != "" && slices.Contains(snapshot.Processes[holder.PID].ContainerIDs, id)
	})
}

// nativeOwners groups the remaining sockets by process family.
func nativeOwners(snapshot domain.Snapshot, sockets []domain.Socket) []domain.Owner {
	var owners []domain.Owner
	hidden := map[string]int{}
	for _, pid := range rootsOf(snapshot, domain.HolderPIDs(sockets)) {
		owners = append(owners, processOwner(snapshot, pid, sockets))
	}
	for _, socket := range sockets {
		if len(socket.Holders) > 0 {
			continue
		}
		index, seen := hidden[socket.CreatorUID]
		if !seen {
			index = len(owners)
			hidden[socket.CreatorUID] = index
			owners = append(owners, hiddenOwner(snapshot.Host, socket.CreatorUID))
		}
		owners[index].Listeners = append(owners[index].Listeners, socket)
	}
	return owners
}

// rootsOf keeps holders that have no holder above them.
func rootsOf(snapshot domain.Snapshot, pids []int) []int {
	var roots []int
	for _, pid := range pids {
		ancestors, _ := snapshot.Ancestors(pid)
		hasHolderAbove := slices.ContainsFunc(ancestors, func(p domain.Process) bool { return slices.Contains(pids, p.PID) })
		if !hasHolderAbove {
			roots = append(roots, pid)
		}
	}
	return roots
}

// processOwner describes a holder and the workers below it.
func processOwner(snapshot domain.Snapshot, pid int, sockets []domain.Socket) domain.Owner {
	process := snapshot.Processes[pid]
	owner := domain.Owner{Type: kindOf(process), Access: domain.AccessOK, User: process.User, Process: &process}
	family := append([]int{pid}, snapshot.Descendants(pid)...)
	for _, socket := range sockets {
		holders := domain.HolderPIDs([]domain.Socket{socket})
		isMine := false
		for _, holder := range holders {
			isFamily := slices.Contains(family, holder)
			isMine = isMine || isFamily
			if isFamily && holder != pid && !slices.Contains(owner.Workers, holder) {
				owner.Workers = append(owner.Workers, holder)
			}
		}
		if isMine {
			owner.Listeners = append(owner.Listeners, socket)
		}
	}
	slices.Sort(owner.Workers)
	return owner
}

// hiddenOwner describes sockets whose process is not visible.
func hiddenOwner(host domain.Host, userID string) domain.Owner {
	owner := domain.Owner{Type: "Native", Access: domain.AccessUnknown, User: UserName(userID)}
	if userID != "" && userID != host.CallerUID && !host.Privileged {
		owner.Access = domain.AccessDenied
	}
	return owner
}

// UserName resolves a user ID, or returns it unchanged.
func UserName(id string) string {
	if entry, err := user.LookupId(id); err == nil && id != "" {
		return entry.Username
	}
	return id
}

// runtimes maps program name prefixes to display kinds.
var runtimes = []struct{ prefix, kind string }{
	{"node", "Node"}, {"npm", "Node"}, {"bun", "Bun"}, {"deno", "Deno"}, {"python", "Python"}, {"uvicorn", "Python"},
	{"gunicorn", "Python"}, {"flask", "Python"}, {"java", "Java"}, {"ruby", "Ruby"}, {"puma", "Ruby"}, {"php", "PHP"}, {"dotnet", ".NET"},
}

// kindOf names the runtime of a process, for display only.
func kindOf(process domain.Process) string {
	path := strings.ToLower(strings.ReplaceAll(process.Exe, `\`, "/"))
	name := path[strings.LastIndexByte(path, '/')+1:]
	if name == "" {
		name = strings.ToLower(process.Name)
	}
	for _, runtime := range runtimes {
		if strings.HasPrefix(name, runtime.prefix) {
			return runtime.kind
		}
	}
	return "Native"
}

// addProjects detects each process once, in parallel, inside the deadline.
func (a *App) addProjects(ctx context.Context, snapshot domain.Snapshot, reports []domain.PortReport) {
	var workers sync.WaitGroup
	found := map[int]*domain.Project{}
	var mu sync.Mutex
	started := map[int]bool{}
	eachNative(reports, func(owner *domain.Owner) {
		if started[owner.Process.PID] {
			return
		}
		started[owner.Process.PID] = true
		process := *owner.Process
		workers.Go(func() {
			project := a.detect(ctx, snapshot, process)
			mu.Lock()
			defer mu.Unlock()
			found[process.PID] = project
		})
	})
	workers.Wait()
	eachNative(reports, func(owner *domain.Owner) { owner.Project = found[owner.Process.PID] })
}

// eachNative visits owners that are processes.
func eachNative(reports []domain.PortReport, visit func(*domain.Owner)) {
	for i := range reports {
		for j := range reports[i].Owners {
			if reports[i].Owners[j].Process != nil {
				visit(&reports[i].Owners[j])
			}
		}
	}
}

// detect finds one process's project.
func (a *App) detect(ctx context.Context, snapshot domain.Snapshot, process domain.Process) *domain.Project {
	ancestors, _ := snapshot.Ancestors(process.PID)
	return a.projects.Detect(ctx, process, ancestors)
}

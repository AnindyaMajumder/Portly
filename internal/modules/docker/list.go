package docker

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

const inspectWorkers = 8

var containerID = regexp.MustCompile(`^[0-9a-f]{64}$`)

var errMalformed = errors.New("malformed engine data")

// listed is the part of /containers/json Portly needs.
type listed struct {
	ID     string `json:"Id"`
	Names  []string
	Image  string
	State  string
	Labels map[string]string
	Ports  []struct {
		IP                      string
		PrivatePort, PublicPort int
		Type                    string
	}
	HostConfig struct{ NetworkMode string }
}

// inspected is the part of /containers/ID/json Portly needs.
type inspected struct {
	ID    string `json:"Id"`
	State struct {
		Running, Paused bool
		Pid             int
		StartedAt       string
	}
	Config     struct{ StopSignal string }
	HostConfig struct {
		AutoRemove  bool
		NetworkMode string
	}
}

// list returns the engine kind and its running containers.
func (a *api) list(ctx context.Context) (string, []domain.Container, domain.Answer) {
	var version struct {
		Components []struct{ Name string }
	}
	if err := a.get(ctx, "/version", &version); err != nil {
		return "", nil, answerFor(err)
	}
	kind := "docker"
	for _, component := range version.Components {
		if strings.HasPrefix(component.Name, "Podman") {
			kind = "podman"
		}
	}
	var entries []listed
	if err := a.get(ctx, "/containers/json", &entries); err != nil {
		return kind, nil, answerFor(err)
	}
	containers := make([]domain.Container, len(entries))
	for i, entry := range entries {
		container, err := entry.container()
		if err != nil {
			return kind, nil, domain.AnswerMalformed
		}
		containers[i] = container
	}
	addSharedNetworks(entries, containers)
	a.inspectAll(ctx, containers)
	return kind, containers, domain.AnswerOK
}

// container validates one listed entry.
func (l listed) container() (domain.Container, error) {
	if !containerID.MatchString(l.ID) {
		return domain.Container{}, errMalformed
	}
	container := domain.Container{
		ID: l.ID, Image: l.Image, Running: l.State == "running" || l.State == "paused", Paused: l.State == "paused",
		HostNet: l.HostConfig.NetworkMode == "host", Orchestrator: orchestrator(l.Labels),
		ComposeProject: firstLabel(l.Labels, "com.docker.compose.project", "io.podman.compose.project"),
		ComposeService: firstLabel(l.Labels, "com.docker.compose.service", "io.podman.compose.service"),
		Directory:      l.Labels["com.docker.compose.project.working_dir"],
	}
	if len(l.Names) > 0 {
		container.Name = strings.TrimPrefix(l.Names[0], "/")
	}
	for _, port := range l.Ports {
		// Exposed only, or a protocol Portly does not know
		if port.PublicPort == 0 || port.Type != "tcp" && port.Type != "udp" {
			continue
		}
		address, err := netip.ParseAddr(port.IP)
		isInvalid := port.IP != "" && err != nil || port.PublicPort < 0 || port.PublicPort > 65535 || port.PrivatePort < 0 || port.PrivatePort > 65535
		if isInvalid {
			return domain.Container{}, errMalformed
		}
		container.Ports = append(container.Ports, domain.Binding{
			HostAddr: address, HostPort: uint16(port.PublicPort), ContainerPort: uint16(port.PrivatePort), Protocol: domain.Protocol(port.Type),
		})
	}
	return container, nil
}

// firstLabel returns the first label that is set.
func firstLabel(labels map[string]string, keys ...string) string {
	for _, key := range keys {
		if labels[key] != "" {
			return labels[key]
		}
	}
	return ""
}

// orchestrator names whatever would restart or owns the container.
func orchestrator(labels map[string]string) string {
	for label := range labels {
		switch {
		case strings.HasPrefix(label, "com.docker.swarm."):
			return "swarm"
		case strings.HasPrefix(label, "io.kubernetes."), label == "io.x-k8s.kind.cluster":
			return "kubernetes"
		case label == "PODMAN_SYSTEMD_UNIT":
			return "systemd"
		}
	}
	return ""
}

// addSharedNetworks records containers living in another one's network.
func addSharedNetworks(entries []listed, containers []domain.Container) {
	for _, entry := range entries {
		target, isShared := strings.CutPrefix(entry.HostConfig.NetworkMode, "container:")
		if !isShared {
			continue
		}
		name := entry.ID
		if len(entry.Names) > 0 {
			name = strings.TrimPrefix(entry.Names[0], "/")
		}
		for i := range containers {
			if containers[i].ID == target || containers[i].Name == target {
				containers[i].SharesNet = append(containers[i].SharesNet, name)
			}
		}
	}
}

// inspectAll fills identity and state. A failure leaves Inspected false.
func (a *api) inspectAll(ctx context.Context, containers []domain.Container) {
	slots := make(chan struct{}, inspectWorkers)
	var workers sync.WaitGroup
	for i := range containers {
		if len(containers[i].Ports) == 0 && !containers[i].HostNet {
			continue
		}
		workers.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			found, err := a.inspect(ctx, containers[i].ID)
			if err == nil {
				found.apply(&containers[i])
			}
		})
	}
	workers.Wait()
}

// inspect reads one container and checks it is the one asked for.
func (a *api) inspect(ctx context.Context, id string) (inspected, error) {
	var found inspected
	if !containerID.MatchString(id) {
		return found, errMalformed
	}
	if err := a.get(ctx, "/containers/"+id+"/json", &found); err != nil {
		return found, err
	}
	if _, err := time.Parse(time.RFC3339Nano, found.State.StartedAt); err != nil || found.ID != id {
		return found, errMalformed
	}
	return found, nil
}

// apply copies what inspect knows better than the list.
func (f inspected) apply(container *domain.Container) {
	container.Inspected, container.StartedAt = true, f.State.StartedAt
	container.Running, container.Paused = f.State.Running, f.State.Paused
	container.InitPID, container.StopSignal = f.State.Pid, f.Config.StopSignal
	container.AutoRemove = f.HostConfig.AutoRemove
	container.HostNet = f.HostConfig.NetworkMode == "host"
}

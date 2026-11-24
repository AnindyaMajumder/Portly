package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/app"
	"github.com/AnindyaMajumder/Portly/internal/domain"
	"github.com/AnindyaMajumder/Portly/internal/platform/textsafe"
)

const labelWidth = 11

// renderDetails prints every named port, separated by blank lines.
func (s *session) renderDetails(report app.Report) {
	for i, port := range report.Ports {
		if i > 0 {
			s.println("")
		}
		s.renderPort(port, len(report.Ports) > 1)
	}
}

// renderPort prints one port and what to run next.
func (s *session) renderPort(port domain.PortReport, isOneOfMany bool) {
	if port.Status == domain.NotListening {
		s.println(fmt.Sprintf("Port %s is free.", port.Ref))
		return
	}
	if isOneOfMany || port.Ref.Protocol == domain.UDP {
		s.field("Port", port.Ref.String())
	}
	for i, owner := range port.Owners {
		if i > 0 {
			s.println("")
		}
		s.renderOwner(owner)
	}
	s.println(s.paint(faint, s.nextStep(port)))
}

// nextStep suggests the command that fits.
func (s *session) nextStep(port domain.PortReport) string {
	target := strconv.Itoa(int(port.Ref.Port))
	if port.Ref.Protocol == domain.UDP {
		target = port.Ref.String()
	}
	switch {
	case port.Ambiguous:
		return "Several owners share this port, so stopping it may be refused"
	case port.Status == domain.Inaccessible && s.console.Windows:
		return "To see the owner, run 'portly " + target + "' in an administrator terminal"
	case port.Status == domain.Inaccessible:
		return "To see the owner:  sudo " + quote(s.console.Executable) + " " + target
	case !s.app.CanStop():
		return stopDisabled
	}
	return "Run 'portly stop " + target + "' to stop it"
}

// renderOwner prints what is known, and says what is not.
func (s *session) renderOwner(owner domain.Owner) {
	project := unknownProject
	if owner.Project != nil {
		project = fmt.Sprintf("%s  (%s: %s)", clean(owner.Project.Name), owner.Project.Confidence, clean(owner.Project.Evidence))
	}
	s.field("Project", project)
	switch {
	case owner.Container != nil:
		s.renderContainer(owner)
	case owner.Process != nil:
		s.renderProcess(owner)
	case owner.Access == domain.AccessDenied:
		s.field("Process", deniedOwner)
		s.field("User", clean(owner.User))
	default:
		s.field("Process", "Unknown owner")
	}
	s.field("Address", s.endpoints(owner))
}

// renderProcess prints a process, its command masked.
func (s *session) renderProcess(owner domain.Owner) {
	process := owner.Process
	if owner.Project != nil && owner.Project.Root != process.Dir {
		s.field("Root", s.short(owner.Project.Root))
	}
	s.field("Directory", s.short(process.Dir))
	s.field("Process", fmt.Sprintf("%s (PID %d)", clean(process.Name), process.PID))
	s.field("Command", textsafe.CommandLine(process.Args))
	s.field("User", clean(process.User))
	var workers []string
	for _, pid := range owner.Workers {
		workers = append(workers, strconv.Itoa(pid))
	}
	s.field("Workers", strings.Join(workers, ", "))
}

// renderContainer prints a container and its Compose service.
func (s *session) renderContainer(owner domain.Owner) {
	container := owner.Container
	name := clean(container.Name)
	if container.ComposeService != "" {
		name += "  (" + clean(container.ComposeProject) + " / " + clean(container.ComposeService) + ")"
	}
	s.field("Container", name)
	s.field("Image", imageName(container.Image))
	s.field("Directory", s.short(container.Directory))
	engine := owner.Type
	if !owner.Proven {
		engine += ", not proven to run on this machine"
	}
	s.field("Engine", engine)
}

const shortDigest = len("sha256:") + 12

// imageName shortens the digest of an untagged image.
func imageName(image string) string {
	if strings.HasPrefix(image, "sha256:") && len(image) > shortDigest {
		image = image[:shortDigest]
	}
	return clean(image)
}

// endpoints lists every bound address with its port.
func (s *session) endpoints(owner domain.Owner) string {
	var found []string
	for _, socket := range owner.Listeners {
		found = append(found, clean(socket.ID.String()))
	}
	if len(found) == 0 && owner.Container != nil {
		return "published by the engine, no host socket"
	}
	return strings.Join(found, ", ")
}

// field prints a label and value, nothing when the value is unknown.
func (s *session) field(label, value string) {
	if value == "" {
		return
	}
	s.println(s.paint(faint, fmt.Sprintf("%-*s", labelWidth, label)) + value)
}

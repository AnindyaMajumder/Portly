package cli

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/AnindyaMajumder/Portly/internal/app"
	"github.com/AnindyaMajumder/Portly/internal/domain"
	"github.com/AnindyaMajumder/Portly/internal/platform/textsafe"
)

// Basic colours follow the user's own terminal theme
const (
	red    = "1"
	green  = "2"
	yellow = "3"
	faint  = "8"
	blue   = "4"
)

const (
	dash       = "—"
	columnGap  = 2
	minProject = 8
)

// clean makes process-derived text safe to print.
func clean(text string) string { return textsafe.Clean(text) }

// paint colours text only when the console wants it.
func (s *session) paint(color, text string) string {
	if !s.console.Color || text == "" {
		return text
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(text)
}

// println prints one line of data.
func (s *session) println(text string) { fmt.Fprintln(s.console.Out, text) }

// short replaces the home directory with ~.
func (s *session) short(path string) string {
	home := s.console.Home
	if home != "" && (path == home || strings.HasPrefix(path, home+string(filepath.Separator))) {
		path = "~" + path[len(home):]
	}
	return clean(path)
}

// columns of the list. The first five always show.
var columns = []string{"PORT", "PROTO", "PROJECT", "TYPE", "PID", "PROCESS", "ADDRESS", "DIRECTORY"}

const (
	projectColumn  = 2
	alwaysColumns  = 5
	processColumn  = 5
	unknownProject = "Unknown"
)

// renderList prints one row per owner.
func (s *session) renderList(report app.Report) {
	if len(report.Ports) == 0 {
		s.println("No listening ports found.")
	}
	var rows [][]string
	for _, port := range report.Ports {
		for _, owner := range port.Owners {
			rows = append(rows, s.row(port, owner))
		}
	}
	if len(rows) > 0 {
		s.renderTable(rows)
	}
	if s.console.Width > 0 {
		s.println(s.paint(faint, footer(report)))
	}
}

// footer counts ports and says what to run next.
func footer(report app.Report) string {
	var parts []string
	if count := len(report.Ports); count > 0 {
		parts = append(parts, plural(count, "port")+" found")
	}
	if report.HiddenUDP > 0 {
		parts = append(parts, fmt.Sprintf("%d UDP hidden (portly --all)", report.HiddenUDP))
	}
	if len(report.Ports) > 0 {
		parts = append(parts, fmt.Sprintf("Run 'portly %d' for details", example(report.Ports)))
	}
	return strings.Join(parts, " · ")
}

// example prefers a port the user can inspect fully.
func example(ports []domain.PortReport) uint16 {
	for _, port := range ports {
		if len(port.Owners) == 1 && port.Owners[0].Project != nil {
			return port.Ref.Port
		}
	}
	return ports[0].Ref.Port
}

// plural counts things.
func plural(count int, noun string) string {
	if count == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(count) + " " + noun + "s"
}

// row is one owner of one port.
func (s *session) row(port domain.PortReport, owner domain.Owner) []string {
	project, pid, process, directory := unknownProject, dash, dash, dash
	if owner.Project != nil {
		project, directory = clean(owner.Project.Name), s.short(owner.Project.Root)
	}
	switch {
	case owner.Container != nil:
		process = clean(owner.Container.Name)
	case owner.Process != nil:
		pid, process = strconv.Itoa(owner.Process.PID), clean(owner.Process.Name)
		if owner.Process.Dir != "" {
			directory = s.short(owner.Process.Dir)
		}
	case owner.Access == domain.AccessDenied:
		process = "Permission denied"
	default:
		process = "Unknown owner"
	}
	if directory == "" {
		directory = dash
	}
	number, protocol := strconv.Itoa(int(port.Ref.Port)), string(port.Ref.Protocol)
	return []string{number, protocol, project, owner.Type, pid, process, addresses(owner, " "), directory}
}

// addresses lists where an owner listens, without ports.
func addresses(owner domain.Owner, separator string) string {
	var found []string
	seen := map[string]bool{}
	for _, socket := range owner.Listeners {
		address := socket.ID.Addr.Unmap().WithZone(socket.ID.Scope).String()
		if !seen[address] {
			seen[address] = true
			found = append(found, address)
		}
	}
	if len(found) == 0 {
		return dash
	}
	return clean(strings.Join(found, separator))
}

// renderTable drops columns from the right until the table fits.
func (s *session) renderTable(rows [][]string) {
	widths := make([]int, len(columns))
	for _, cells := range append([][]string{columns}, rows...) {
		for i, cell := range cells {
			widths[i] = max(widths[i], lipgloss.Width(cell))
		}
	}
	shown := s.fit(widths)
	s.println(s.paint(faint, s.line(columns, widths, shown)))
	for _, cells := range rows {
		s.println(s.colorRow(cells, widths, shown))
	}
}

// fit returns how many columns show, and narrows the project as a last resort.
func (s *session) fit(widths []int) int {
	if s.console.Width == 0 {
		return len(widths)
	}
	total := func(shown int) int {
		sum := columnGap * (shown - 1)
		for _, width := range widths[:shown] {
			sum += width
		}
		return sum
	}
	shown := len(widths)
	for shown > alwaysColumns && total(shown) > s.console.Width {
		shown--
	}
	if excess := total(shown) - s.console.Width; excess > 0 {
		widths[projectColumn] = max(minProject, widths[projectColumn]-excess)
	}
	return shown
}

// line joins padded cells.
func (s *session) line(cells []string, widths []int, shown int) string {
	var padded []string
	for i, cell := range cells[:shown] {
		padded = append(padded, pad(cell, widths[i]))
	}
	return strings.TrimRight(strings.Join(padded, strings.Repeat(" ", columnGap)), " ")
}

// colorRow highlights the port and dims what could not be read.
func (s *session) colorRow(cells []string, widths []int, shown int) string {
	if !s.console.Color {
		return s.line(cells, widths, shown)
	}
	var padded []string
	for i, cell := range cells[:shown] {
		text := pad(cell, widths[i])
		switch {
		case i == 0:
			text = s.paint(blue, text)
		case cell == dash || cell == unknownProject:
			text = s.paint(faint, text)
		case i == processColumn && cells[4] == dash && strings.HasPrefix(cell, "Permission"):
			text = s.paint(yellow, text)
		}
		padded = append(padded, text)
	}
	return strings.Join(padded, strings.Repeat(" ", columnGap))
}

// pad fills or cuts a cell to a display width.
func pad(text string, width int) string {
	for lipgloss.Width(text) > width {
		runes := []rune(text)
		cut := max(len(runes)-2, 0)
		text = string(runes[:cut]) + "…"
		if cut == 0 {
			break
		}
	}
	return text + strings.Repeat(" ", max(width-lipgloss.Width(text), 0))
}

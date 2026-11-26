package cli

import (
	"fmt"
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
	dash         = "—"
	columnGap    = 2
	minProject   = 8
	minDirectory = 12
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

// paintErr colours text on stderr, which may go elsewhere.
func (s *session) paintErr(color, text string) string {
	if !s.console.ErrColor || text == "" {
		return text
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(text)
}

// println prints one line of data.
func (s *session) println(text string) { fmt.Fprintln(s.console.Out, text) }

// short replaces the home directory with ~.
func (s *session) short(path string) string {
	home := s.console.Home
	rest, isInside := strings.CutPrefix(path, home)
	if home != "" && isInside && (rest == "" || rest[0] == '/' || rest[0] == '\\') {
		path = "~" + rest
	}
	return clean(path)
}

// columns of the list. The first five always show.
var columns = []string{"PORT", "PROTO", "PROJECT", "TYPE", "PID", "PROCESS", "ADDRESS", "DIRECTORY"}

const (
	projectColumn   = 2
	alwaysColumns   = 5
	processColumn   = 5
	unknownProject  = "Unknown"
	deniedOwner     = "Permission denied"
	addressColumn   = 6
	maxAddress      = 24
	directoryColumn = 7
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
		process = deniedOwner
	default:
		process = "Unknown owner"
	}
	if directory == "" {
		directory = dash
	}
	number, protocol := strconv.Itoa(int(port.Ref.Port)), string(port.Ref.Protocol)
	return []string{number, protocol, project, owner.Type, pid, process, addresses(owner), directory}
}

// addresses lists where an owner listens, without ports.
func addresses(owner domain.Owner) string {
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
	return clean(strings.Join(found, " "))
}

// renderTable drops columns from the right until the table fits.
func (s *session) renderTable(rows [][]string) {
	widths := make([]int, len(columns))
	for _, cells := range append([][]string{columns}, rows...) {
		for i, cell := range cells {
			widths[i] = max(widths[i], lipgloss.Width(cell))
		}
	}
	if s.console.Width > 0 {
		widths[addressColumn] = min(widths[addressColumn], maxAddress)
	}
	shown := s.fit(widths)
	isFramed := s.frame().left != ""
	if isFramed {
		s.println(s.rule("╭", "┬", "╮", widths[:shown]))
	}
	s.println(s.paint(faint, s.line(columns, widths, shown)))
	if isFramed {
		s.println(s.rule("├", "┼", "┤", widths[:shown]))
	}
	for _, cells := range rows {
		s.println(s.colorRow(cells, widths, shown))
	}
	if isFramed {
		s.println(s.rule("╰", "┴", "╯", widths[:shown]))
	}
}

// frame is the border around a table on a terminal. Piped output has none.
type frame struct{ left, gap, right string }

// frame picks the border for this console.
func (s *session) frame() frame {
	if s.console.Width == 0 {
		return frame{gap: strings.Repeat(" ", columnGap)}
	}
	return frame{left: "│ ", gap: " │ ", right: " │"}
}

// width is how many cells the border takes around some columns.
func (f frame) width(shown int) int {
	return lipgloss.Width(f.left) + lipgloss.Width(f.right) + lipgloss.Width(f.gap)*(shown-1)
}

// rule draws a horizontal border line with its corners and joints.
func (s *session) rule(left, joint, right string, widths []int) string {
	var parts []string
	for _, width := range widths {
		parts = append(parts, strings.Repeat("─", width+2))
	}
	return s.paint(faint, left+strings.Join(parts, joint)+right)
}

// fit returns how many columns show, and narrows the project as a last resort.
func (s *session) fit(widths []int) int {
	if s.console.Width == 0 {
		return len(widths)
	}
	total := func(shown int) int {
		sum := s.frame().width(shown)
		for _, width := range widths[:shown] {
			sum += width
		}
		return sum
	}
	shown := len(widths)
	for shown > alwaysColumns && total(shown) > s.console.Width {
		shown--
	}
	// A long directory takes the room left instead of disappearing
	if room := s.console.Width - total(directoryColumn+1) + widths[directoryColumn]; shown == directoryColumn && room >= minDirectory {
		widths[directoryColumn], shown = room, directoryColumn+1
	}
	if excess := total(shown) - s.console.Width; excess > 0 {
		widths[projectColumn] = max(min(minProject, widths[projectColumn]), widths[projectColumn]-excess)
	}
	return shown
}

// line joins padded cells inside the border.
func (s *session) line(cells []string, widths []int, shown int) string {
	var padded []string
	for i, cell := range cells[:shown] {
		padded = append(padded, fitCell(i, cell, widths[i]))
	}
	f := s.frame()
	text := f.left + strings.Join(padded, f.gap) + f.right
	if f.left == "" {
		return strings.TrimRight(text, " ")
	}
	return text
}

// colorRow highlights the port and dims what could not be read.
func (s *session) colorRow(cells []string, widths []int, shown int) string {
	if !s.console.Color {
		return s.line(cells, widths, shown)
	}
	var padded []string
	for i, cell := range cells[:shown] {
		text := fitCell(i, cell, widths[i])
		switch {
		case i == 0:
			text = s.paint(blue, text)
		case cell == dash || cell == unknownProject:
			text = s.paint(faint, text)
		case i == processColumn && cells[4] == dash && strings.HasPrefix(cell, deniedOwner):
			text = s.paint(yellow, text)
		}
		padded = append(padded, text)
	}
	f := s.frame()
	return s.paint(faint, f.left) + strings.Join(padded, s.paint(faint, f.gap)) + s.paint(faint, f.right)
}

// fitCell pads a cell, shortening directories in the middle instead of the end.
func fitCell(column int, cell string, width int) string {
	if column == directoryColumn {
		cell = fitPath(cell, width)
	}
	return pad(cell, width)
}

// fitPath keeps a path's start and as many trailing folders as fit, like ~/…/acme/web.
func fitPath(path string, width int) string {
	if lipgloss.Width(path) <= width {
		return path
	}
	separator := "/"
	if strings.Contains(path, `\`) && !strings.Contains(path, "/") {
		separator = `\`
	}
	parts := strings.Split(path, separator)
	head, tail := parts[0]+separator+"…", ""
	for i := len(parts) - 1; i > 0 && lipgloss.Width(head+separator+parts[i]+tail) <= width; i-- {
		tail = separator + parts[i] + tail
	}
	if tail != "" {
		return head + tail
	}
	last := []rune(parts[len(parts)-1])
	for len(last) > 0 && lipgloss.Width("…"+string(last)) > width {
		last = last[1:]
	}
	return "…" + string(last)
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

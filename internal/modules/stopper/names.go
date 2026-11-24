package stopper

import (
	"path"
	"slices"
	"strings"

	"github.com/AnindyaMajumder/Portly/internal/domain"
)

// The kernel cuts process names to this length
const maxCommLength = 15

// Names only add refusals, they never grant permission
var forwarderNames = []string{
	"docker-proxy", "rootlessport", "rootlessport-child", "rootlesskit", "pasta", "pasta.avx2", "passt", "passt.avx2",
	"slirp4netns", "vpnkit", "vpnkit-bridge", "conmon", "conmonrs", "com.docker.backend", "com.docker.vpnkit",
	"lima", "limactl", "colima", "gvproxy", "orbstack", "orbstack helper", "wslrelay", "sshd", "sshd-session",
}

var vmManagerNames = []string{"limactl", "lima", "colima", "podman", "orbstack", "vagrant"}

// Processes that hold a published port for an engine on this machine
var engineNames = []string{
	"dockerd", "docker-proxy", "podman", "conmon", "rootlessport", "rootlessport-child", "rootlesskit", "pasta",
	"pasta.avx2", "slirp4netns",
}

// An engine in a VM counts only through its own forwarder
var vmForwarders = []struct {
	socket string
	names  []string
}{
	{"/.docker/run/", []string{"com.docker.backend", "com.docker.vpnkit"}},
	{"/.docker/desktop/", []string{"com.docker.backend", "com.docker.vpnkit"}},
	{"/.orbstack/run/", []string{"orbstack", "orbstack helper"}},
	{"/.colima/", []string{"limactl"}},
	{"/podman/machine/", []string{"gvproxy"}},
	{`\\.\pipe\docker`, []string{"com.docker.backend"}},
	{`\\.\pipe\podman-machine`, []string{"gvproxy", "wslrelay"}},
}

var supervisorNames = []string{
	"nodemon", "pm2", "supervisord", "runit", "runsv", "runsvdir", "s6-supervise", "s6-svscan", "s6-svc", "air", "watchexec",
}

var watchRuntimes = []string{"node", "bun", "deno"}

// How many arguments may name the script an interpreter runs
const maxScriptArgs = 4

// isForwarder matches programs that hold ports for something else.
func isForwarder(process domain.Process, ancestors []domain.Process) bool {
	names := programNames(process)
	if matchesAny(names, forwarderNames) {
		return true
	}
	if !matchesAny(names, []string{"ssh"}) {
		return false
	}
	// A shared VM connection detaches, so only its arguments tell
	if slices.ContainsFunc(process.Args, isVMConnection) {
		return true
	}
	return slices.ContainsFunc(ancestors, func(ancestor domain.Process) bool {
		return matchesAny(programNames(ancestor), vmManagerNames)
	})
}

// isVMConnection matches ssh titles and options of VM managers.
func isVMConnection(argument string) bool {
	return strings.Contains(argument, "[mux]") || slices.ContainsFunc(vmManagerNames, func(name string) bool {
		return strings.Contains(strings.ToLower(argument), name)
	})
}

// holderNames lists programs that may hold a published port for an engine.
func holderNames(engine domain.Engine, isLocal bool) []string {
	if isLocal {
		return engineNames
	}
	for _, forwarder := range vmForwarders {
		if strings.Contains(engine.ID, forwarder.socket) {
			return forwarder.names
		}
	}
	return nil
}

// isSupervisor matches programs that restart what they run.
func isSupervisor(process domain.Process) bool {
	if matchesAny(programNames(process), supervisorNames) || matchesAny(scriptNames(process.Args), supervisorNames) {
		return true
	}
	isWatching := slices.ContainsFunc(process.Args, func(arg string) bool {
		return arg == "--watch" || strings.HasPrefix(arg, "--watch=") || strings.HasPrefix(arg, "--watch-path")
	})
	return isWatching && matchesAny(programNames(process), watchRuntimes)
}

// programNames lists every name a process goes by.
func programNames(process domain.Process) []string {
	names := []string{normalName(process.Name), normalName(strings.TrimSuffix(process.Exe, " (deleted)"))}
	if len(process.Args) > 0 {
		// Titles look like "sshd: user" or "PM2 v5: God Daemon (/path)"
		title, _, _ := strings.Cut(process.Args[0], ":")
		first, _, _ := strings.Cut(title, " ")
		names = append(names, normalName(process.Args[0]), normalName(title), normalName(first))
	}
	return names
}

// scriptNames lists scripts and modules an interpreter was started with.
func scriptNames(args []string) []string {
	var names []string
	for i, arg := range args {
		if i == 0 || i > maxScriptArgs || strings.HasPrefix(arg, "-") {
			continue
		}
		first, _, _ := strings.Cut(arg, " ")
		name := normalName(first)
		names = append(names, name, strings.TrimSuffix(name, path.Ext(name)))
		if dot := strings.LastIndexByte(name, '.'); args[i-1] == "-m" && dot >= 0 {
			names = append(names, name[dot+1:])
		}
	}
	return names
}

// normalName is the lower-case base name on any OS.
func normalName(name string) string {
	name = strings.ToLower(strings.ReplaceAll(name, `\`, "/"))
	return strings.TrimSuffix(name[strings.LastIndexByte(name, '/')+1:], ".exe")
}

// matchesAny also matches names the kernel cut short.
func matchesAny(names, wanted []string) bool {
	for _, name := range names {
		for _, want := range wanted {
			isCut := len(name) == maxCommLength && strings.HasPrefix(want, name)
			if name == want || isCut {
				return true
			}
		}
	}
	return false
}

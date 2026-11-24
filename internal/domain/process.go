package domain

// UnitKind is the innermost service manager unit type.
type UnitKind uint8

const (
	UnitUnknown UnitKind = iota
	UnitNone
	UnitService
	UnitScope
)

// Unit is the systemd unit a process runs in.
type Unit struct {
	Name      string
	User      bool
	Kind      UnitKind
	MainPID   int
	MainKnown bool
}

// Job is the launchd job a process or its ancestor runs as.
type Job struct {
	Label       string
	MainPID     int
	Application bool
	// False when a launchd domain could not be read
	Known bool
}

// Process is what the OS lets us see. Empty means unknown.
type Process struct {
	ProcessID
	PPID      int
	SessionID int
	RealUID   string
	User      string
	Home      string
	Name      string
	Dir       string
	Args      []string
	// Whether every descriptor of this process could be read
	Sockets Completeness

	TTY         Tri
	Kernel      bool
	Zombie      bool
	Suspended   bool
	UserManager bool
	Tracer      Tri

	// How SIGTERM is handled
	Caught  Tri
	Ignored Tri
	Blocked Tri

	// Outermost container first
	ContainerIDs []string
	// Cgroup path up to the outermost container, which a name can't forge
	ContainerPath string
	Unit          Unit
	Job           Job
	// Windows service or session 0
	Service Tri
}

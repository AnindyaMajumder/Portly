package domain

type Confidence string

const (
	High   Confidence = "high"
	Medium Confidence = "medium"
)

// Project is a detected source directory. A nil project is unknown.
type Project struct {
	Name       string
	Root       string
	Confidence Confidence
	Evidence   string
}

type Access string

const (
	AccessOK      Access = "ok"
	AccessDenied  Access = "permission_denied"
	AccessUnknown Access = "unknown"
)

// Owner is one process family or container behind a port.
type Owner struct {
	// Display kind: Node, Python, Docker, Native
	Type      string
	Access    Access
	User      string
	Process   *Process
	Container *Container
	// Container engine proven to run on this machine
	Proven    bool
	Project   *Project
	Workers   []int
	Listeners []Socket
}

type Status string

const (
	Listening    Status = "listening"
	Published    Status = "published"
	NotListening Status = "not_listening"
	Inaccessible Status = "inaccessible"
)

// PortReport is everything known about one port.
type PortReport struct {
	Ref       PortRef
	Status    Status
	Ambiguous bool
	Owners    []Owner
}

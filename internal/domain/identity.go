package domain

// Completeness starts at the value that blocks a stop.
type Completeness uint8

const (
	Unavailable Completeness = iota
	Partial
	Complete
)

// String is the name used in JSON.
func (c Completeness) String() string {
	switch c {
	case Complete:
		return "complete"
	case Partial:
		return "partial"
	}
	return "unavailable"
}

// Tri keeps "not observed" apart from "no".
type Tri uint8

const (
	Unknown Tri = iota
	No
	Yes
)

// TriOf converts an observed fact.
func TriOf(fact bool) Tri {
	if fact {
		return Yes
	}
	return No
}

// ProcessID answers: which process is this?
type ProcessID struct {
	PID    int
	Start  string
	UserID string
	Exe    string
}

// Same is false when anything is unknown, so it guards PID reuse and exec.
func (p ProcessID) Same(other ProcessID) bool {
	isKnown := p.PID > 0 && p.Start != "" && p.Start != "0" && p.UserID != "" && p.Exe != ""
	return isKnown && p == other
}

// SocketID answers: which socket was observed?
type SocketID struct {
	AddressPort
	Family uint8
	NetNS  string
	OSID   string
}

// Same is false when the OS id or namespace is unknown.
func (s SocketID) Same(other SocketID) bool {
	isKnown := s.OSID != "" && s.OSID != "0" && s.NetNS != "" && s.Addr.IsValid()
	return isKnown && s == other
}

// Holder is one descriptor of one process holding a socket.
type Holder struct{ PID, FD int }

// Evidence says where ownership came from and how complete it is.
type Evidence struct {
	Source       string
	Completeness Completeness
}

// Socket is a listening TCP socket or a bound UDP endpoint.
type Socket struct {
	ID         SocketID
	CreatorUID string
	Connected  bool
	Holders    []Holder
	Evidence   []Evidence
}

// Completeness is the weakest evidence, unavailable when there is none.
func (s Socket) Completeness() Completeness {
	if len(s.Evidence) == 0 {
		return Unavailable
	}
	weakest := Complete
	for _, evidence := range s.Evidence {
		weakest = min(weakest, evidence.Completeness)
	}
	return weakest
}

// HeldBy reports whether a process holds the socket.
func (s Socket) HeldBy(pid int) bool {
	for _, holder := range s.Holders {
		if holder.PID == pid {
			return true
		}
	}
	return false
}

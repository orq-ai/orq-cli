package skills

// LinkState is what is actually at a recorded link's path, as opposed to what
// the manifest claims. Deriving it anywhere but here is how `orq doctor` came
// to call a path healthy that refresh had already stopped touching: existence
// is not ownership, and only one of the two is what the projector acts on.
type LinkState string

const (
	// LinkInstalled means the path holds our own projection: a symlink into
	// our snapshot, or a copy carrying our marker.
	LinkInstalled LinkState = "installed"
	// LinkMissing means nothing is at the path. Between CLI updates nothing
	// puts it back — refresh only reprojects when the fingerprint moves.
	LinkMissing LinkState = "missing"
	// LinkForeign means something else occupies the path. Every projector
	// refuses to touch it, so the link is recorded and permanently inert.
	LinkForeign LinkState = "foreign"
)

// LinkStatus is one recorded permanent link and the state of its path.
type LinkStatus struct {
	Path  string
	Agent string
	State LinkState
}

// Status is the state of everything the manifest records, for any caller that
// reports on skills rather than projecting them.
//
// Session links are excluded: a live `orq launch` creates and destroys them,
// and their absence between sessions is not breakage.
type Status struct {
	Links []LinkStatus
	// Stale reports that the recorded fingerprint is behind this CLI's, so
	// the installed set is from an older version. Refresh repairs it, but
	// only on the commands that touch skills (see skillsCommand in
	// register.go), so someone who updates the CLI and opens their agent
	// directly stays on the old set until they run one.
	Stale bool
}

// ReadStatus reports what the manifest records and what is really on disk. It
// returns nil when this machine has never connected: no manifest is not a
// state to report, it is the absence of one.
func ReadStatus() (*Status, error) {
	m, err := LoadManifest()
	if err != nil || m == nil {
		return nil, err
	}
	s := &Status{Stale: m.Fingerprint != Fingerprint()}
	for _, l := range m.Links {
		if l.Session {
			continue
		}
		s.Links = append(s.Links, LinkStatus{Path: l.Path, Agent: l.Agent, State: linkState(l)})
	}
	return s, nil
}

func linkState(l Link) LinkState {
	switch {
	case !exists(l.Path):
		return LinkMissing
	case !isOurs(l):
		return LinkForeign
	default:
		return LinkInstalled
	}
}

// Count returns how many links are in the given state.
func (s *Status) Count(state LinkState) int {
	n := 0
	for _, l := range s.Links {
		if l.State == state {
			n++
		}
	}
	return n
}

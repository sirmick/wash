package swarm

// RecordUsage checkpoints provider context accounting at turn/retirement
// boundaries, including already-ended members. Telemetry does not advance the
// coordination revision; streamed usage is carried separately in live frames.
func (s *Store) RecordUsage(session string, used, size int64) error {
	if session == "" || used < 0 || size < 0 || used == 0 && size == 0 {
		return nil
	}
	return s.change(func(st *State) error {
		for i := len(st.Workspaces) - 1; i >= 0; i-- {
			if st.Workspaces[i].State == "ended" {
				continue
			}
			for j := range st.Workspaces[i].Members {
				m := &st.Workspaces[i].Members[j]
				if m.Session == session {
					m.Usage = &Usage{Used: used, Size: size}
					return nil
				}
			}
		}
		return nil
	})
}

package chat

// Entry is a user message or a successful assistant reply in this session.
type Entry struct {
	Role    string
	Content string
}

// History returns a copy of the session's entries in conversation order.
// Failed replies leave only the submitted user message in history.
func (s *Session) History() []Entry {
	return append([]Entry(nil), s.history...)
}

// ClearHistory removes all entries without changing the active model.
func (s *Session) ClearHistory() {
	s.history = nil
}

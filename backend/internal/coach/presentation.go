package coach

// Public preserves frozen grading facts. Legacy plain-text references can be
// presented using current Markdown only when its entire normalized body matches
// the frozen reference byte-for-byte. This never writes a migration or a grade.
func (s *Service) Public(c Conversation) Conversation {
	out := c.Public()
	if out.Plan == nil || s.knowledge == nil {
		return out
	}
	entries := s.knowledge.Snapshots()
	for i := range out.Plan.Questions {
		q := &out.Plan.Questions[i]
		if q.Reference == "" || q.ReferenceMarkdown != "" {
			continue
		}
		for _, e := range entries {
			if e.ID == q.ID && e.Source == q.Source && e.Excerpt == q.Reference && RawHash(e.Excerpt) == q.ReferenceHash {
				q.ReferenceMarkdown = e.Markdown
				break
			}
		}
	}
	return out
}

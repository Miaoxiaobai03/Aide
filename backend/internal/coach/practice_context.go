package coach

// The independent-page builder receives one persisted page, never a whole
// practice that is subsequently filtered. UI directories are not model memory.
func (s *Service) buildQuestionContext(c Conversation, in Input, purpose string, mandatoryOnly bool) (Projection, []Message, error) {
	if c.PracticeID == "" || len(c.Plan.Questions) != 1 || c.Plan.Current != 0 || in.QuestionID != "" && in.QuestionID != c.Plan.Questions[0].ID {
		return Projection{}, nil, fail("question_scope", "请切换到对应题页再提问", 422)
	}
	for _, m := range c.Messages {
		if m.Ordinal != 1 {
			return Projection{}, nil, fail("question_scope", "本题记录包含其他题来源，已阻止模型调用", 422)
		}
	}
	for _, key := range append(append([]string{}, in.References...), in.Corrects) {
		if key == "" {
			continue
		}
		found := false
		for _, m := range c.Messages {
			if m.ID == key && !m.Deleted {
				found = true
				break
			}
		}
		if !found {
			return Projection{}, nil, fail("question_scope", "引用或纠正不属于本题，请切换到对应题页再提问", 422)
		}
	}
	for _, f := range in.Fragments {
		found := false
		for _, m := range c.Messages {
			if m.ID == f.MessageID && !m.Deleted {
				found = true
				break
			}
		}
		if !found {
			return Projection{}, nil, fail("question_scope", "引用片段不属于本题，请切换到对应题页再提问", 422)
		}
	}
	c.QuestionGroups = nil
	return s.projectMaterials(c, in, purpose, mandatoryOnly)
}

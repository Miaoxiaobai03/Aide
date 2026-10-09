package coach

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"aide/backend/internal/knowledge"
)

func TestReadableReferenceDisclosureAndLegacyRecovery(t *testing.T) {
	s, _, _ := setup(t)
	root := t.TempDir()
	markdown := "## Q：如何恢复失败请求？\n\n**恢复步骤**\n\n1. 保留原始回答。\n2. 校验提交ID。\n\n| 状态 | 行为 |\n|---|---|\n| 失败 | 不计零分 |\n"
	if err := os.WriteFile(filepath.Join(root, "a.md"), []byte(markdown), 0600); err != nil {
		t.Fatal(err)
	}
	index, err := knowledge.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	s.knowledge = index
	c, err := s.StartPractice(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if c.Plan.Questions[0].ReferenceMarkdown == "" {
		t.Fatal("lost Markdown in frozen snapshot")
	}
	if public := s.Public(c); public.Plan.Questions[0].Reference != "" || public.Plan.Questions[0].ReferenceMarkdown != "" {
		t.Fatal("unanswered reference leaked")
	}
	c, err = s.Learn(context.Background(), c.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if s.Public(c).Plan.Questions[0].ReferenceMarkdown == "" {
		t.Fatal("learned reference is not readable")
	}
	// Simulate an old snapshot without changing its grading content or hash.
	c.Plan.Questions[0].ReferenceMarkdown = ""
	c.Plan.Questions[0].Hash = ""
	c.Plan.Questions[0].Hash = Hash(c.Plan.Questions[0])
	before := Hash(c)
	if s.Public(c).Plan.Questions[0].ReferenceMarkdown == "" || Hash(c) != before {
		t.Fatal("legacy display failed or mutated original")
	}
	c.Plan.Questions[0].Reference += " 冻结后不同内容"
	c.Plan.Questions[0].ReferenceHash = RawHash(c.Plan.Questions[0].Reference)
	if s.Public(c).Plan.Questions[0].ReferenceMarkdown != "" {
		t.Fatal("recovered Markdown from a different source version")
	}
	if len(index.Entries()) != 1 || index.Entries()[0].Markdown != "" {
		t.Fatal("public search exposed full Markdown")
	}
}

package coach

import (
	"regexp"
	"sort"
	"strings"
)

var recallTerms = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9_]{1,}|[\p{Han}]{2,}`)
var genericTerms = map[string]bool{"请问": true, "解释": true, "为什么": true, "什么": true, "怎么": true, "如何": true, "之前": true, "刚才": true, "那个": true, "问题": true, "回答": true, "请继续": true}

// Lightweight lexical recall only. Exact message/ordinal selection remains the
// reliable path. Snippets retain source ID/revision/hash and contiguous text;
// retrieval is neither a score nor proof that the topic is relevant.
func recallEvidence(c Conversation, query string, mandatory, covered, fragments map[string]bool) []Evidence {
	terms := []string{}
	for _, term := range recallTerms.FindAllString(strings.ToLower(query), -1) {
		if genericTerms[term] {
			continue
		}
		terms = append(terms, term)
	}
	if len(terms) == 0 {
		return nil
	}
	type hit struct {
		message       Message
		score, offset int
	}
	hits := []hit{}
	for _, m := range c.Messages {
		if m.Deleted || mandatory[m.ID] || fragments[m.ID] || !covered[m.ID] || m.Status != "complete" {
			continue
		}
		text := strings.ToLower(m.Content)
		score, offset := 0, -1
		for _, term := range terms {
			at := strings.Index(text, term)
			if at >= 0 {
				score += len([]rune(term))
				if offset < 0 {
					offset = at
				}
			}
		}
		if score >= 2 {
			hits = append(hits, hit{m, score, offset})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score == hits[j].score {
			return hits[i].message.Seq > hits[j].message.Seq
		}
		return hits[i].score > hits[j].score
	})
	out := []Evidence{}
	for _, h := range hits[:min(3, len(hits))] {
		chars := []rune(h.message.Content)
		at := len([]rune(strings.ToLower(h.message.Content)[:h.offset]))
		start := max(0, at-100)
		end := min(len(chars), at+200)
		out = append(out, Evidence{h.message.ID, string(chars[start:end]), Hash(h.message), h.message.Revision})
	}
	return out
}

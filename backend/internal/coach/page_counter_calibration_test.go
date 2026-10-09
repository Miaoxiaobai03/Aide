package coach

import (
	"bytes"
	"encoding/json"
	"math"
	"offerpilot/backend/internal/chat"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Reads archived provider results offline. Missing archives in another checkout
// skip calibration rather than reaching a network or generating new fixtures.
func TestPageCounterAgainstArchivedProviderUsage(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "benchmark", "runs", "question-memory-20261008", "collection", "calls")
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skip("archived provider calibration corpus unavailable")
	}
	var oldErrors, newErrors []float64
	maxUnder := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".request.json") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var req struct {
			Model    string         `json:"model"`
			Messages []chat.Message `json:"messages"`
			Output   int            `json:"max_tokens"`
		}
		if err = json.Unmarshal(bytes.TrimPrefix(b, []byte{239, 187, 191}), &req); err != nil {
			return err
		}
		if req.Model != "deepseek-flash" {
			return nil
		}
		response, err := os.ReadFile(strings.TrimSuffix(path, ".request.json") + ".response.txt")
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		actual := 0
		for _, line := range strings.Split(string(response), "\n") {
			if !strings.HasPrefix(line, "data: {") {
				continue
			}
			var event struct {
				Usage *struct {
					Input int `json:"prompt_tokens"`
				} `json:"usage"`
			}
			if err = json.Unmarshal([]byte(line[6:]), &event); err != nil {
				return err
			}
			if event.Usage != nil && event.Usage.Input > 0 {
				actual = event.Usage.Input
			}
		}
		if actual == 0 {
			return nil
		}
		old, _ := (EstimateCounter{}).Count(req.Model, req.Messages, req.Output)
		fresh, _ := (PracticeEstimateCounter{}).Count(req.Model, req.Messages, req.Output)
		oldErrors = append(oldErrors, math.Abs(float64(old-actual))/float64(actual))
		newErrors = append(newErrors, math.Abs(float64(fresh-actual))/float64(actual))
		maxUnder = max(maxUnder, actual-fresh)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(newErrors) == 0 {
		t.Fatal("no provider usage pairs")
	}
	sort.Float64s(oldErrors)
	sort.Float64s(newErrors)
	n := len(newErrors)
	p95 := int(math.Ceil(float64(n)*.95)) - 1
	if maxUnder > 0 || newErrors[n/2] >= oldErrors[n/2] || newErrors[p95] >= oldErrors[p95] {
		t.Fatal("calibrated counter regressed or underestimated measured usage")
	}
	t.Logf("offline provider pairs=%d oldMedian=%.6f newMedian=%.6f oldP95=%.6f newP95=%.6f maxUnderestimate=%d", n, oldErrors[n/2], newErrors[n/2], oldErrors[p95], newErrors[p95], maxUnder)
}

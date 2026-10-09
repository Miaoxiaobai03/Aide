package jobextract

import (
	"context"
	"testing"

	"aide/backend/internal/harness"
	"aide/backend/internal/llm"
)

type visionStub struct {
	text   string
	images []llm.ImageInput
}

func (s *visionStub) ChatJSON(context.Context, []llm.Message, any) error { return nil }
func (s *visionStub) ChatJSONWithImages(_ context.Context, _ []llm.Message, images []llm.ImageInput, out any) error {
	s.images = images
	*(out.(*Result)) = Result{Text: s.text}
	return nil
}

func TestJDTranscriberUsesOneImageAndRejectsEmptyResult(t *testing.T) {
	client := &visionStub{text: " 岗位职责\n熟悉 Go "}
	runtime, err := harness.NewRuntime(client, harness.Options{})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(runtime)
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.Extract(context.Background(), "data:image/png;base64,aGVsbG8=")
	if err != nil || result != "岗位职责\n熟悉 Go" || len(client.images) != 1 || client.images[0].Detail != "high" {
		t.Fatalf("result=%q images=%#v err=%v", result, client.images, err)
	}
	client.text = "抱歉，我无法查看您提供的图片"
	if _, err := agent.Extract(context.Background(), "data:image/png;base64,aGVsbG8="); err == nil {
		t.Fatal("unreadable image answer should fail")
	}
}

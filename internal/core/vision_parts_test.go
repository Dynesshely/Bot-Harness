package core

import (
	"strings"
	"testing"

	"github.com/dynesshely/bot-harness/internal/store"
)

func TestAnnotateMediaRefsKeepsTheSource(t *testing.T) {
	refs := []MediaRef{
		{MediaID: "cur-1", Source: MediaSourceCurrent, Kind: "image"},
		{MediaID: "quo-1", Source: MediaSourceQuoted, Kind: "image"},
		{MediaID: "cur-1", Source: MediaSourceCurrent, Kind: "image"}, // duplicate
		{MediaID: "", Source: MediaSourceCurrent, Kind: "image"},      // unusable
	}
	got := annotateMediaRefs("看看这些图", refs)
	for _, want := range []string{"[图片 #cur-1]", "[引用图片 #quo-1]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少标注 %q：%q", want, got)
		}
	}
	if strings.Count(got, "cur-1") != 1 {
		t.Fatalf("同一个 media id 只能标注一次：%q", got)
	}
	if !strings.HasPrefix(got, "看看这些图") {
		t.Fatalf("标注应追加在正文之后：%q", got)
	}
}

func TestAnnotateMediaRefsWithoutImagesLeavesTextAlone(t *testing.T) {
	if got := annotateMediaRefs("纯文字", nil); got != "纯文字" {
		t.Fatalf("无图片时不应改变正文：%q", got)
	}
	if got := annotateMediaRefs("", []MediaRef{{MediaID: "id", Source: MediaSourceCurrent, Kind: "image"}}); got != "[图片 #id]" {
		t.Fatalf("空正文应只留下标注：%q", got)
	}
}

func TestAnnotateHistoryCopiesAndAddsIDs(t *testing.T) {
	original := []store.Message{{
		Role: "user", Content: "上一轮的图",
		Attachments: []store.Attachment{
			{Kind: "image", MediaID: "hist-1", ContentType: "image/png"},
			{Kind: "file", MediaID: "doc-1"},
		},
	}}
	annotated := annotateHistory(original)
	if strings.Contains(original[0].Content, "hist-1") {
		t.Fatal("annotateHistory 不应修改原始历史")
	}
	if !strings.Contains(annotated[0].Content, "[图片 #hist-1]") {
		t.Fatalf("历史图片应带 id：%q", annotated[0].Content)
	}
	if strings.Contains(annotated[0].Content, "doc-1") {
		t.Fatalf("非图片附件不应被标注：%q", annotated[0].Content)
	}
}

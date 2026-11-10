package qq

import "testing"

func TestQuotedAttachmentsWalksMsgElements(t *testing.T) {
	event := &MessageEvent{
		// A quoted image lives in msg_elements, not in the top-level attachments.
		Attachments: []MessageAttachment{{URL: "https://cdn.example/current.png"}},
		MsgElements: []MsgElement{
			{
				Content:     "引用的话",
				Attachments: []MessageAttachment{{URL: "https://cdn.example/quoted.png"}},
				MsgElements: []MsgElement{{
					Attachments: []MessageAttachment{{URL: "https://cdn.example/merged.png"}},
				}},
			},
		},
	}
	got := event.QuotedAttachments()
	if len(got) != 2 {
		t.Fatalf("应展开两层 msg_elements，实际 %#v", got)
	}
	if got[0].URL != "https://cdn.example/quoted.png" || got[1].URL != "https://cdn.example/merged.png" {
		t.Fatalf("顺序或内容不对：%#v", got)
	}
}

func TestQuotedAttachmentsEmpty(t *testing.T) {
	if got := (*MessageEvent)(nil).QuotedAttachments(); got != nil {
		t.Fatalf("nil event 应返回 nil，实际 %#v", got)
	}
	if got := (&MessageEvent{Attachments: []MessageAttachment{{URL: "a"}}}).QuotedAttachments(); got != nil {
		t.Fatalf("顶层附件不算引用，实际 %#v", got)
	}
}

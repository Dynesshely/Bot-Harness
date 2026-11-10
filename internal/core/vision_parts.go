package core

import (
	"context"
	"strings"

	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/store"
)

// 图片引用标注：把缓存下来的图片 id 以稳定文本形式告诉模型，模型才能按需
// 调用 view_image。标注只进入**模型输入**，不写进会话消息正文。

// visionSystemPrompt is appended to the system prompt when the model can call
// view_image. It intentionally says "only when needed": the current message's
// images are already sent multimodally, so calling the tool for every picture
// only burns tokens.
const visionSystemPrompt = `消息里的 [图片 #id] 或 [引用图片 #id] 是已缓存的图片引用。需要了解某张图片的内容时，可调用 view_image（参数 media_id，可选 question）获取描述；只在图片对回答确实重要时调用，不要逐图调用，也不要编造不存在或没有 id 的图片。`

// isImageAttachment reports whether a stored attachment is an image we can hand
// to a visual model.
func isImageAttachment(a store.Attachment) bool {
	if a.Kind == "image" {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(a.ContentType)), "image/")
}

// ingestAttachments caches the current message's attachments and every
// attachment carried by quote/merge elements. The two groups are ingested
// separately so the model-facing annotations can keep the current|quoted
// distinction; duplicates (same URL) are fetched once.
//
// It must only be called when d.media != nil.
func (d *Dispatcher) ingestAttachments(ctx context.Context, current, quoted []qq.MessageAttachment) ([]store.Attachment, []MediaRef) {
	var (
		attachments []store.Attachment
		refs        []MediaRef
	)
	seenURL := make(map[string]bool)

	add := func(items []qq.MessageAttachment, source string) {
		if len(items) == 0 {
			return
		}
		filtered := make([]qq.MessageAttachment, 0, len(items))
		for _, item := range items {
			key := strings.TrimSpace(item.URL)
			if key != "" {
				if seenURL[key] {
					continue
				}
				seenURL[key] = true
			}
			filtered = append(filtered, item)
		}
		if len(filtered) == 0 {
			return
		}
		stored := d.media.Ingest(ctx, filtered)
		for _, item := range stored {
			attachments = append(attachments, item)
			if !isImageAttachment(item) || strings.TrimSpace(item.MediaID) == "" {
				continue
			}
			refs = append(refs, MediaRef{MediaID: item.MediaID, Source: source, Kind: "image"})
		}
	}

	add(current, MediaSourceCurrent)
	add(quoted, MediaSourceQuoted)
	return attachments, refs
}

// inboundMediaRefs returns the image references of one inbound message. When
// the dispatcher did not populate MediaRefs (tests and older callers build an
// Inbound by hand) every cached attachment is treated as current.
func inboundMediaRefs(in *Inbound) []MediaRef {
	if in == nil {
		return nil
	}
	if len(in.MediaRefs) > 0 {
		return in.MediaRefs
	}
	refs := make([]MediaRef, 0, len(in.Attachments))
	for _, attachment := range in.Attachments {
		if !isImageAttachment(attachment) || strings.TrimSpace(attachment.MediaID) == "" {
			continue
		}
		refs = append(refs, MediaRef{MediaID: attachment.MediaID, Source: MediaSourceCurrent, Kind: "image"})
	}
	return refs
}

// annotateMediaRefs appends the model-facing image annotations to one message.
// The id is the only thing the tool needs; the source word tells the model
// whether the picture is part of this message or of the quoted one.
func annotateMediaRefs(text string, refs []MediaRef) string {
	labels := make([]string, 0, len(refs))
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		id := strings.TrimSpace(ref.MediaID)
		if id == "" || ref.Kind != "image" || seen[id] {
			continue
		}
		seen[id] = true
		if ref.Source == MediaSourceQuoted {
			labels = append(labels, "[引用图片 #"+id+"]")
			continue
		}
		labels = append(labels, "[图片 #"+id+"]")
	}
	if len(labels) == 0 {
		return text
	}
	annotation := strings.Join(labels, " ")
	if strings.TrimSpace(text) == "" {
		return annotation
	}
	return text + "\n" + annotation
}

// annotateHistory makes the cached images of earlier messages addressable by
// view_image too. It copies the slice instead of mutating the caller's history:
// the stored message text stays clean, while the model input carries the ids.
func annotateHistory(history []store.Message) []store.Message {
	if len(history) == 0 {
		return history
	}
	out := make([]store.Message, len(history))
	copy(out, history)
	for i := range out {
		refs := make([]MediaRef, 0, len(out[i].Attachments))
		for _, attachment := range out[i].Attachments {
			if !isImageAttachment(attachment) || strings.TrimSpace(attachment.MediaID) == "" {
				continue
			}
			refs = append(refs, MediaRef{MediaID: attachment.MediaID, Source: MediaSourceCurrent, Kind: "image"})
		}
		out[i].Content = annotateMediaRefs(out[i].Content, refs)
	}
	return out
}

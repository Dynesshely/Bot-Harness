package qq

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"
)

// ErrPassiveWindowClosed is returned when the 5-minute passive reply window has
// elapsed, so the caller can fall back to an active message.
var ErrPassiveWindowClosed = errors.New("passive reply window closed")

// Sender delivers replies, honouring the platform's segmentation limits.
type Sender struct {
	api *Client
	log *slog.Logger
	// markdownOffUntil holds a unix-seconds deadline after which markdown is
	// attempted again. A rejected markdown request is cheap to retry, but doing
	// it on every message doubles the platform calls, so one failure parks it.
	markdownOffUntil atomic.Int64
}

// NewSender builds a Sender.
func NewSender(api *Client, logger *slog.Logger) *Sender {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sender{api: api, log: logger}
}

// SendOptions describes one outbound message.
type SendOptions struct {
	GroupOpenID string // exactly one of GroupOpenID / UserOpenID is set
	UserOpenID  string
	MsgID       string // passive reply target; empty sends an active message
	// QuoteMessageID makes the message a quoted reply. It is a platform
	// reference index (REFIDX_…), not a message id: for a message somebody else
	// sent it comes from the event's message_scene.ext[msg_idx], and for one of
	// our own messages from the send response's ext_info.ref_idx.
	QuoteMessageID string
	// Markdown sends msg_type=2 with the text in markdown.content (the plain
	// content field must stay empty). Needs the platform's markdown permission;
	// when it is missing the sender falls back to text automatically.
	Markdown bool
	// SegmentPause is the gap between bubbles of one answer, so a long reply
	// lands as separate messages instead of one wall of text.
	SegmentPause time.Duration
	MaxChars     int
	MaxSegments  int
	// Bubbles optionally supplies pre-split bubbles (one human reply broken into
	// several messages). When set, they replace the maxChars-based first split but
	// still get re-split if a single bubble exceeds MaxChars.
	Bubbles []string
	// BubblePause, when set, computes the wait before segment i (i > 0); it takes
	// precedence over SegmentPause. index is the segment's sequence number - 1.
	BubblePause func(index int, text string) time.Duration
}

// SendResult reports what was actually delivered.
type SendResult struct {
	MessageIDs []string `json:"message_ids"`
	Segments   int      `json:"segments"`
	Truncated  bool     `json:"truncated"`
	Passive    bool     `json:"passive"`
	LatencyMS  int64    `json:"latency_ms"`
	// RefIdx is the reference index of the delivered message, needed to quote it
	// later on. Empty when the platform did not report one.
	RefIdx string `json:"ref_idx,omitempty"`
}

// Send splits text and posts it as one or more platform messages.
//
// Passive replies use msg_seq 1..N so the platform accepts repeated replies to
// the same msg_id; that also makes duplicates detectable.
func (s *Sender) Send(ctx context.Context, opts SendOptions, text string) (*SendResult, error) {
	maxChars := opts.MaxChars
	if maxChars < 50 {
		maxChars = 1200
	}
	maxSegments := opts.MaxSegments
	if maxSegments < 1 {
		maxSegments = 3
	}
	if opts.MsgID != "" && maxSegments > 5 {
		// A passive reply happens inside one msg_id, and the platform allows at
		// most 5 replies per message. Active messages carry no such ceiling, so
		// the cap only applies to the passive path.
		maxSegments = 5
	}
	if opts.MsgID == "" && maxSegments > 20 {
		maxSegments = 20
	}

	var segments []string
	if len(opts.Bubbles) > 0 {
		for _, bubble := range opts.Bubbles {
			for _, part := range SplitText(bubble, maxChars) {
				segments = append(segments, part)
			}
		}
	} else {
		segments = SplitText(text, maxChars)
	}
	truncated := false
	if len(segments) == 0 {
		return &SendResult{}, nil
	}
	if len(segments) > maxSegments {
		segments = segments[:maxSegments]
		truncated = true
		last := segments[len(segments)-1]
		const suffix = "\n…（内容过长，已截断）"
		if runeLen(last)+runeLen(suffix) <= maxChars {
			segments[len(segments)-1] = last + suffix
		} else {
			segments[len(segments)-1] = truncateRunes(last, maxChars-runeLen(suffix)) + suffix
		}
	}

	start := time.Now()
	res := &SendResult{Passive: opts.MsgID != "", Truncated: truncated, Segments: len(segments)}
	for i, seg := range segments {
		pause := opts.SegmentPause
		if i > 0 && opts.BubblePause != nil {
			pause = opts.BubblePause(i-1, seg)
		}
		if i > 0 && pause > 0 {
			select {
			case <-time.After(pause):
			case <-ctx.Done():
				res.LatencyMS = time.Since(start).Milliseconds()
				return res, ctx.Err()
			}
		}
		req := s.buildRequest(opts, seg, i+1)

		var (
			resp *SendMessageResponse
			err  error
		)
		if opts.GroupOpenID != "" {
			resp, err = s.api.SendGroupMessage(ctx, opts.GroupOpenID, req)
		} else {
			resp, err = s.api.SendC2CMessage(ctx, opts.UserOpenID, req)
		}

		switch {
		case err == nil:
			res.MessageIDs = append(res.MessageIDs, resp.ID)
			if res.RefIdx == "" && resp.ExtInfo.RefIdx != "" {
				// Remember how to quote this message later.
				res.RefIdx = resp.ExtInfo.RefIdx
			}
		case IsDuplicate(err):
			// msg_id+msg_seq already delivered: treat as success, do not retry.
			s.log.Warn("duplicate reply suppressed by platform", "component", "sender", "msg_seq", req.MsgSeq)
			res.MessageIDs = append(res.MessageIDs, "")
		case IsMarkdownUnavailable(err):
			// No markdown permission (or a rejected markdown body): park markdown
			// and resend this segment as plain text so the answer still arrives.
			s.disableMarkdown()
			req = s.buildRequest(SendOptions{MsgID: opts.MsgID, QuoteMessageID: opts.QuoteMessageID}, seg, i+1)
			if opts.GroupOpenID != "" {
				resp, err = s.api.SendGroupMessage(ctx, opts.GroupOpenID, req)
			} else {
				resp, err = s.api.SendC2CMessage(ctx, opts.UserOpenID, req)
			}
			if err != nil {
				res.LatencyMS = time.Since(start).Milliseconds()
				return res, err
			}
			res.MessageIDs = append(res.MessageIDs, resp.ID)
			if res.RefIdx == "" && resp.ExtInfo.RefIdx != "" {
				res.RefIdx = resp.ExtInfo.RefIdx
			}
		case IsPassiveExpired(err):
			res.LatencyMS = time.Since(start).Milliseconds()
			return res, errors.Join(ErrPassiveWindowClosed, err)
		default:
			res.LatencyMS = time.Since(start).Milliseconds()
			return res, err
		}
	}
	res.LatencyMS = time.Since(start).Milliseconds()
	return res, nil
}

// SendImage delivers one PNG as a rich-media message (msg_type=7).
//
// The platform requires uploading the bytes first and then sending a message
// that references the returned file_info; there is no single-shot image
// endpoint. One image is one message, so a passive reply always uses msg_seq=1
// (the quote reference, when present, rides on that first message).
//
// Unlike Send, platform errors are returned unchanged: the caller decides
// whether to fall back to text, because only it knows what the text version was.
func (s *Sender) SendImage(ctx context.Context, opts SendOptions, png []byte) (*SendResult, error) {
	if len(png) == 0 {
		return &SendResult{}, &APIError{Path: "image", Message: "empty image"}
	}

	start := time.Now()
	var (
		fileInfo string
		err      error
	)
	if opts.GroupOpenID != "" {
		fileInfo, err = s.api.UploadGroupImage(ctx, opts.GroupOpenID, png)
	} else {
		fileInfo, err = s.api.UploadC2CImage(ctx, opts.UserOpenID, png)
	}
	if err != nil {
		return &SendResult{LatencyMS: time.Since(start).Milliseconds()}, err
	}

	req := SendMessageRequest{
		MsgType: MsgTypeMedia,
		Media:   &MediaInfo{FileInfo: fileInfo},
	}
	if opts.MsgID != "" {
		req.MsgID = opts.MsgID
		req.MsgSeq = 1
	}
	if opts.QuoteMessageID != "" {
		req.MessageRef = &MessageRef{MessageID: opts.QuoteMessageID}
	}

	res := &SendResult{Passive: opts.MsgID != "", Segments: 1}
	var resp *SendMessageResponse
	if opts.GroupOpenID != "" {
		resp, err = s.api.SendGroupMessage(ctx, opts.GroupOpenID, req)
	} else {
		resp, err = s.api.SendC2CMessage(ctx, opts.UserOpenID, req)
	}
	if err != nil {
		res.LatencyMS = time.Since(start).Milliseconds()
		return res, err
	}
	res.MessageIDs = append(res.MessageIDs, resp.ID)
	res.RefIdx = resp.ExtInfo.RefIdx
	res.LatencyMS = time.Since(start).Milliseconds()
	return res, nil
}

// buildRequest assembles one platform message. Markdown goes in
// markdown.content and leaves the plain content empty — the platform rejects the
// request otherwise. Quoting is applied to the first segment only, since
// repeating it would stack several quote blocks under one answer.
func (s *Sender) buildRequest(opts SendOptions, text string, seq int) SendMessageRequest {
	req := SendMessageRequest{}
	if opts.Markdown && s.MarkdownUsable() {
		req.MsgType = MsgTypeMarkdown
		req.Markdown = &MessageMarkdown{Content: text}
	} else {
		req.MsgType = MsgTypeText
		req.Content = text
	}
	if opts.MsgID != "" {
		req.MsgID = opts.MsgID
		req.MsgSeq = seq
	}
	if seq == 1 && opts.QuoteMessageID != "" {
		req.MessageRef = &MessageRef{MessageID: opts.QuoteMessageID}
	}
	return req
}

// markdownCooldown is how long a rejected markdown request parks the feature.
const markdownCooldown = 24 * time.Hour

// MarkdownUsable reports whether markdown may be attempted right now.
func (s *Sender) MarkdownUsable() bool {
	return time.Now().Unix() >= s.markdownOffUntil.Load()
}

func (s *Sender) disableMarkdown() {
	until := time.Now().Add(markdownCooldown).Unix()
	if s.markdownOffUntil.Swap(until) < until {
		s.log.Warn("markdown rejected by the platform; falling back to plain text",
			"component", "sender", "cooldown_minutes", int(markdownCooldown.Minutes()))
	}
}

// --- text splitting ---

// SplitText breaks text into chunks of at most maxChars runes, preferring
// paragraph then sentence boundaries and keeping fenced code blocks balanced
// across chunks.
func SplitText(text string, maxChars int) []string {
	if maxChars < 50 {
		maxChars = 50
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	normalized = strings.TrimSpace(normalized)
	if normalized == "" {
		return nil
	}
	if runeLen(normalized) <= maxChars {
		return []string{normalized}
	}

	var (
		out     []string
		cur     strings.Builder
		inFence bool
	)

	curLen := func() int { return runeLen(cur.String()) }

	flush := func() {
		body := strings.TrimRight(cur.String(), "\n")
		if body == "" {
			cur.Reset()
			return
		}
		if inFence {
			body += "\n```"
		}
		out = append(out, body)
		cur.Reset()
		if inFence {
			cur.WriteString("```\n")
		}
	}

	appendPiece := func(piece string) {
		s := cur.String()
		if s != "" && !strings.HasSuffix(s, "\n") {
			cur.WriteString("\n")
		}
		cur.WriteString(piece)
	}

	for _, para := range strings.Split(normalized, "\n\n") {
		para = strings.TrimRight(para, "\n")
		if para == "" {
			continue
		}
		// Track fence state line by line so chunk boundaries stay valid.
		pieces := strings.Split(para, "\n")
		for _, line := range pieces {
			fenceLine := strings.HasPrefix(strings.TrimSpace(line), "```")
			lineLen := runeLen(line)

			if curLen() > 0 && curLen()+1+lineLen > maxChars {
				flush()
			}
			if lineLen > maxChars {
				// A single over-long line (e.g. a long URL): hard wrap it.
				parts := hardWrap(line, maxChars)
				for i, part := range parts {
					appendPiece(part)
					if i < len(parts)-1 {
						flush()
					}
				}
			} else {
				appendPiece(line)
			}
			if fenceLine {
				inFence = !inFence
			}
		}
		// Prefer paragraph boundaries.
		if curLen() > 0 {
			cur.WriteString("\n\n")
		}
	}

	if body := strings.TrimSpace(cur.String()); body != "" {
		out = append(out, body)
	}
	return compact(out)
}

func compact(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func hardWrap(line string, maxChars int) []string {
	runes := []rune(line)
	var out []string
	for len(runes) > maxChars {
		cut := maxChars
		// Prefer a space or a sentence boundary near the limit.
		for i := maxChars - 1; i > maxChars/2; i-- {
			if runes[i] == ' ' || strings.ContainsRune("。！？!?.;；、,", runes[i]) {
				cut = i + 1
				break
			}
		}
		out = append(out, strings.TrimSpace(string(runes[:cut])))
		runes = runes[cut:]
	}
	if len(runes) > 0 {
		out = append(out, strings.TrimSpace(string(runes)))
	}
	return out
}

func runeLen(s string) int { return len([]rune(s)) }

func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/store"
)

// sendToolImage delivers a tool-rendered PNG as an active message. Tool images
// are intentionally not passive: the final answer still needs the passive
// sequence slots, and a tool may be called before we know whether there will be
// an early acknowledgement. Active delivery keeps the two paths independent.
func (d *Dispatcher) sendToolImage(ctx context.Context, in *Inbound, conv convTarget, png []byte, alt string) error {
	if d.sender == nil {
		return fmt.Errorf("sender is not configured")
	}
	if len(png) == 0 {
		return fmt.Errorf("empty image")
	}
	opts := qq.SendOptions{MaxSegments: 1}
	if in.Scene == "c2c" {
		opts.UserOpenID = in.UserOpenID
	} else {
		opts.GroupOpenID = in.GroupOpenID
	}
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	_, err := d.sender.SendImage(sendCtx, opts, png)
	rec := &store.Record{
		Direction:      "out",
		Scene:          in.Scene,
		ConversationID: conv.ID,
		GroupOpenID:    in.GroupOpenID,
		MemberOpenID:   in.MemberOpenID,
		MemberName:     in.MemberName,
		Content:        strings.TrimSpace(alt),
		Status:         "replied",
		Delivery:       store.DeliveryActive,
		LatencyMS:      time.Since(start).Milliseconds(),
	}
	if rec.Content == "" {
		rec.Content = "[图片] Markdown"
	}
	if err != nil {
		rec.Status = "failed"
		rec.Error = err.Error()
	}
	if id, addErr := d.st.AddRecord(ctx, rec); addErr == nil {
		rec.ID = id
	}
	d.publish("message", rec)
	if err != nil {
		return err
	}
	return nil
}

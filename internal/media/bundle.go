package media

import (
	"context"
	"log/slog"

	"github.com/dynesshely/bot-harness/internal/qq"
	"github.com/dynesshely/bot-harness/internal/store"
)

// Bundle wires the media store and the downloader together, and converts a
// platform attachment into a storable one.
type Bundle struct {
	Store      *Store
	Downloader *Downloader
	Log        *slog.Logger
	// MaxImages caps how many images per message are fetched, to bound both
	// latency and the tokens a single turn can cost.
	MaxImages int
}

// NewBundle builds a Bundle.
func NewBundle(st *Store, dl *Downloader, logger *slog.Logger, maxImages int) *Bundle {
	if logger == nil {
		logger = slog.Default()
	}
	if maxImages <= 0 {
		maxImages = 2
	}
	return &Bundle{Store: st, Downloader: dl, Log: logger, MaxImages: maxImages}
}

// Classify maps a platform content type onto our coarse kind.
func Classify(contentType string) string {
	switch {
	case len(contentType) >= 6 && contentType[:6] == "image/":
		return "image"
	case contentType == "voice":
		return "voice"
	case len(contentType) >= 6 && contentType[:6] == "video/":
		return "video"
	default:
		return "file"
	}
}

// Ingest converts platform attachments into stored ones.
//
// Images are downloaded and cached (the platform URL carries an expiring rkey,
// so the timeline would otherwise rot); everything else is recorded as metadata
// only. Failures are logged and skipped rather than failing the message.
func (b *Bundle) Ingest(ctx context.Context, attachments []qq.MessageAttachment) []store.Attachment {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]store.Attachment, 0, len(attachments))
	images := 0
	for _, a := range attachments {
		item := store.Attachment{
			Kind:        Classify(a.ContentType),
			ContentType: a.ContentType,
			Filename:    a.Filename,
			Width:       a.Width,
			Height:      a.Height,
			Size:        a.Size,
			ASRText:     a.ASRReferText,
		}
		if item.Kind == "image" && a.URL != "" && b.Store != nil && b.Downloader != nil {
			if images >= b.MaxImages {
				b.Log.Info("skipping extra image attachment (per-message cap)",
					"component", "media", "cap", b.MaxImages)
				out = append(out, item)
				continue
			}
			images++
			contentType, data, err := b.Downloader.Fetch(ctx, a.URL)
			if err != nil {
				b.Log.Warn("failed to cache an image attachment", "component", "media", "error", err.Error())
				out = append(out, item)
				continue
			}
			id, err := b.Store.Put(ctx, contentType, data)
			if err != nil {
				b.Log.Warn("failed to store an image attachment", "component", "media", "error", err.Error())
				out = append(out, item)
				continue
			}
			item.MediaID = id
			item.ContentType = contentType
			if item.Size == 0 {
				item.Size = int64(len(data))
			}
		}
		out = append(out, item)
	}
	return out
}

// Load returns the cached bytes of an image attachment, if present.
func (b *Bundle) Load(ctx context.Context, mediaID string) (string, []byte, error) {
	if b == nil || b.Store == nil || mediaID == "" {
		return "", nil, nil
	}
	return b.Store.Get(ctx, mediaID)
}

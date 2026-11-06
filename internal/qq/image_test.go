package qq

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// uploadRequest mirrors the platform's /files body. It is decoded here instead
// of reusing the production struct so the wire shape is pinned independently.
type uploadRequest struct {
	FileType   int    `json:"file_type"`
	FileName   string `json:"file_name"`
	FileData   string `json:"file_data"`
	SrvSendMsg bool   `json:"srv_send_msg"`
}

type imageCall struct {
	Path string
	Body uploadRequest
}

type imagePlatform struct {
	mu       sync.Mutex
	uploads  []imageCall
	messages []struct {
		Path string
		Body SendMessageRequest
	}
	uploadStatus int
}

// newImageTestPlatform wires a real Client/Sender against a fake QQ platform
// that records uploads and messages. The token endpoint is answered inline so
// each test only cares about /files and /messages.
func newImageTestPlatform(t *testing.T) (*imagePlatform, *Sender, *Client) {
	t.Helper()
	p := &imagePlatform{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/getAppAccessToken":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"test-token","expires_in":"7200"}`)
		case strings.HasSuffix(r.URL.Path, "/files"):
			if r.Method != http.MethodPost {
				t.Errorf("upload method = %s, want POST", r.Method)
			}
			var body uploadRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode upload body: %v", err)
			}
			p.mu.Lock()
			p.uploads = append(p.uploads, imageCall{Path: r.URL.Path, Body: body})
			status := p.uploadStatus
			p.mu.Unlock()
			if status != 0 {
				w.WriteHeader(status)
				_, _ = io.WriteString(w, `{"code":40034001,"message":"upload rejected"}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"file_info":"FILEINFO-1","ttl":3600}`)
		case strings.Contains(r.URL.Path, "/messages"):
			var body SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode message body: %v", err)
			}
			p.mu.Lock()
			p.messages = append(p.messages, struct {
				Path string
				Body SendMessageRequest
			}{Path: r.URL.Path, Body: body})
			p.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"MSG-OUT-IMAGE","timestamp":"2026-11-04T10:00:00+08:00","ext_info":{"ref_idx":"REFIDX_IMG"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL, NewTokenManager("app-id", "app-secret", srv.URL, srv.Client()), srv.Client())
	return p, NewSender(client, nil), client
}

func (p *imagePlatform) snapshot() ([]imageCall, []struct {
	Path string
	Body SendMessageRequest
}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	uploads := make([]imageCall, len(p.uploads))
	copy(uploads, p.uploads)
	messages := make([]struct {
		Path string
		Body SendMessageRequest
	}, len(p.messages))
	copy(messages, p.messages)
	return uploads, messages
}

func samplePNG() []byte {
	return []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03}
}

func assertPNGUpload(t *testing.T, call imageCall, wantPath string, wantPNG []byte) {
	t.Helper()
	if call.Path != wantPath {
		t.Fatalf("upload path = %q, want %q", call.Path, wantPath)
	}
	if call.Body.FileType != 1 {
		t.Fatalf("file_type = %d, want 1", call.Body.FileType)
	}
	if call.Body.FileName != "status.png" {
		t.Fatalf("file_name = %q, want status.png", call.Body.FileName)
	}
	if call.Body.SrvSendMsg {
		t.Fatal("srv_send_msg must be false: the caller sends the media message")
	}
	raw, err := base64.StdEncoding.DecodeString(call.Body.FileData)
	if err != nil {
		t.Fatalf("file_data is not base64: %v", err)
	}
	if string(raw) != string(wantPNG) {
		t.Fatalf("file_data decoded to % x, want % x", raw, wantPNG)
	}
	wantHeader := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}
	if len(raw) < len(wantHeader) || !bytes.Equal(raw[:len(wantHeader)], wantHeader) {
		t.Fatalf("decoded bytes do not start with a PNG header: % x", raw)
	}
}

// Both scopes share the same body; only the path differs.
func TestUploadImageRequestShape(t *testing.T) {
	ctx := context.Background()
	png := samplePNG()

	p, _, client := newImageTestPlatform(t)
	if _, err := client.UploadGroupImage(ctx, "G1", png); err != nil {
		t.Fatalf("UploadGroupImage: %v", err)
	}
	if _, err := client.UploadC2CImage(ctx, "U1", png); err != nil {
		t.Fatalf("UploadC2CImage: %v", err)
	}

	uploads, _ := p.snapshot()
	if len(uploads) != 2 {
		t.Fatalf("uploads = %d, want 2", len(uploads))
	}
	assertPNGUpload(t, uploads[0], "/v2/groups/G1/files", png)
	assertPNGUpload(t, uploads[1], "/v2/users/U1/files", png)
}

// TestSendImageGroupWire covers both scopes and pins the media message body.
func TestSendImageGroupWire(t *testing.T) {
	ctx := context.Background()
	png := samplePNG()

	p, sender, _ := newImageTestPlatform(t)
	res, err := sender.SendImage(ctx, SendOptions{
		GroupOpenID:    "G1",
		MsgID:          "MSG-IN-1",
		QuoteMessageID: "REFIDX-QUOTE",
	}, png)
	if err != nil {
		t.Fatalf("SendImage: %v", err)
	}
	if !res.Passive || len(res.MessageIDs) != 1 || res.MessageIDs[0] != "MSG-OUT-IMAGE" {
		t.Fatalf("result = %+v", res)
	}

	uploads, messages := p.snapshot()
	if len(uploads) != 1 {
		t.Fatalf("uploads = %d, want 1", len(uploads))
	}
	assertPNGUpload(t, uploads[0], "/v2/groups/G1/files", png)
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	msg := messages[0]
	if msg.Path != "/v2/groups/G1/messages" {
		t.Fatalf("message path = %q", msg.Path)
	}
	// The wire value is pinned at 7; MsgTypeMedia is the named constant for it.
	if msg.Body.MsgType != 7 || MsgTypeMedia != 7 {
		t.Fatalf("msg_type = %d, want 7 (MsgTypeMedia=%d)", msg.Body.MsgType, MsgTypeMedia)
	}
	if msg.Body.Media == nil || msg.Body.Media.FileInfo != "FILEINFO-1" {
		t.Fatalf("media = %+v, want file_info FILEINFO-1", msg.Body.Media)
	}
	if msg.Body.Content != "" {
		t.Fatalf("media message must carry no content: %q", msg.Body.Content)
	}
	if msg.Body.MsgID != "MSG-IN-1" || msg.Body.MsgSeq != 1 {
		t.Fatalf("msg_id/msg_seq = %q/%d, want MSG-IN-1/1", msg.Body.MsgID, msg.Body.MsgSeq)
	}
	if msg.Body.MessageRef == nil || msg.Body.MessageRef.MessageID != "REFIDX-QUOTE" {
		t.Fatalf("message_reference = %+v", msg.Body.MessageRef)
	}

	// Direct messages use the user path; without msg_id the message is active.
	p2, sender2, _ := newImageTestPlatform(t)
	if _, err := sender2.SendImage(ctx, SendOptions{UserOpenID: "U1"}, png); err != nil {
		t.Fatalf("SendImage c2c: %v", err)
	}
	uploads2, messages2 := p2.snapshot()
	if len(uploads2) != 1 || uploads2[0].Path != "/v2/users/U1/files" {
		t.Fatalf("c2c uploads = %+v", uploads2)
	}
	if len(messages2) != 1 || messages2[0].Path != "/v2/users/U1/messages" {
		t.Fatalf("c2c messages = %+v", messages2)
	}
	if messages2[0].Body.MsgID != "" || messages2[0].Body.MsgSeq != 0 {
		t.Fatalf("active media message must not carry msg_id/msg_seq: %+v", messages2[0].Body)
	}
	if messages2[0].Body.Media == nil || messages2[0].Body.Media.FileInfo != "FILEINFO-1" {
		t.Fatalf("c2c media = %+v", messages2[0].Body.Media)
	}
}

func TestSendImageUploadFailureReturnsAPIError(t *testing.T) {
	ctx := context.Background()
	p, sender, _ := newImageTestPlatform(t)
	p.mu.Lock()
	p.uploadStatus = http.StatusInternalServerError
	p.mu.Unlock()

	res, err := sender.SendImage(ctx, SendOptions{GroupOpenID: "G1", MsgID: "MSG-IN-1"}, samplePNG())
	if err == nil {
		t.Fatal("expected an upload error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %T %v, want *APIError", err, err)
	}
	if apiErr.Code != 40034001 || apiErr.Path != "/v2/groups/G1/files" {
		t.Fatalf("APIError = %+v", apiErr)
	}
	if res == nil {
		t.Fatal("an error still returns the partial result")
	}
	_, messages := p.snapshot()
	if len(messages) != 0 {
		t.Fatalf("no media message may be sent after a failed upload: %+v", messages)
	}

	if _, err := sender.SendImage(ctx, SendOptions{GroupOpenID: "G1"}, nil); err == nil {
		t.Fatal("empty image must fail locally")
	}
}

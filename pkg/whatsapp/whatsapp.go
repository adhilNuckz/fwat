package whatsapp

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"

	qrcode "github.com/skip2/go-qrcode"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"

	"whatsapp-dashboard/pkg/db"

	_ "github.com/mattn/go-sqlite3"
)

// Engine wraps a whatsmeow client with application-level helpers.
type Engine struct {
	Client    *whatsmeow.Client
	DB        *db.DB
	QRChan    chan []byte   // sends PNG bytes of QR code
	Connected chan struct{} // closed when logged in
	mediaDir  string

	connOnce sync.Once
}

// New creates a new Engine. It opens (or creates) the whatsmeow SQLite store,
// retrieves (or creates) the first device, and builds the whatsmeow client.
func New(database *db.DB, mediaDir string) (*Engine, error) {
	// Ensure the data directory exists.
	if err := os.MkdirAll("./data/db", 0o755); err != nil {
		return nil, fmt.Errorf("whatsapp.New: mkdir data/db: %w", err)
	}

	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite3", "file:./data/db/whatsmeow.db?_foreign_keys=on", nil)
	if err != nil {
		return nil, fmt.Errorf("whatsapp.New: open store: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		return nil, fmt.Errorf("whatsapp.New: get device: %w", err)
	}

	client := whatsmeow.NewClient(deviceStore, nil)

	e := &Engine{
		Client:    client,
		DB:        database,
		QRChan:    make(chan []byte, 10),
		Connected: make(chan struct{}),
		mediaDir:  mediaDir,
	}
	return e, nil
}

// Start registers the event handler and connects to WhatsApp.
// If the device is not yet logged in it will emit QR PNG bytes on e.QRChan
// until the pairing succeeds.
func (e *Engine) Start() error {
	e.Client.AddEventHandler(e.handleEvent)

	if e.Client.Store.ID == nil {
		// Not logged in – start QR pairing flow.
		qrChan, err := e.Client.GetQRChannel(context.Background())
		if err != nil {
			return fmt.Errorf("whatsapp.Start: get QR channel: %w", err)
		}

		if err := e.Client.Connect(); err != nil {
			return fmt.Errorf("whatsapp.Start: connect: %w", err)
		}

		go func() {
			for evt := range qrChan {
				switch evt.Event {
				case "code":
					png, err := qrcode.Encode(evt.Code, qrcode.Medium, 256)
					if err != nil {
						log.Printf("whatsapp: QR encode error: %v", err)
						continue
					}
					select {
					case e.QRChan <- png:
					default:
						// Drop if nobody is reading.
					}
				case "success":
					log.Println("whatsapp: QR scan succeeded, logged in")
					e.connOnce.Do(func() { close(e.Connected) })
				case "timeout":
					log.Println("whatsapp: QR code timed out")
				case "error":
					log.Printf("whatsapp: QR error: %v", evt.Error)
				}
			}
		}()
	} else {
		// Already logged in – connect directly.
		if err := e.Client.Connect(); err != nil {
			return fmt.Errorf("whatsapp.Start: connect: %w", err)
		}
		e.connOnce.Do(func() { close(e.Connected) })
	}

	return nil
}

// handleEvent is the whatsmeow event dispatcher.
func (e *Engine) handleEvent(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		e.handleMessage(v)
	case *events.Connected:
		log.Println("whatsapp: connected")
		e.connOnce.Do(func() { close(e.Connected) })
	case *events.Disconnected:
		log.Println("whatsapp: disconnected")
	case *events.LoggedOut:
		log.Println("whatsapp: logged out")
	}
}

// handleMessage processes an incoming message event.
func (e *Engine) handleMessage(evt *events.Message) {
	msg := evt.Message
	info := evt.Info

	// ── Revoke / delete detection ────────────────────────────────────────────
	if pm := msg.GetProtocolMessage(); pm != nil {
		if pm.GetType() == waE2E.ProtocolMessage_REVOKE {
			deletedID := pm.GetKey().GetID()
			if err := e.DB.MarkDeleted(deletedID); err != nil {
				log.Printf("whatsapp: MarkDeleted %q: %v", deletedID, err)
			}
		}
		// Protocol messages don't carry user-visible content; stop here.
		return
	}

	// ── Extract text ─────────────────────────────────────────────────────────
	text := msg.GetConversation()
	if text == "" {
		if ext := msg.GetExtendedTextMessage(); ext != nil {
			text = ext.GetText()
		}
	}

	chatJID := info.Chat.String()
	senderJID := info.Sender.String()
	msgID := info.ID
	ts := info.Timestamp

	// ── Determine media type and download ────────────────────────────────────
	mediaPath := ""
	mediaType := ""

	switch {
	case msg.GetImageMessage() != nil:
		mediaType = "image"
		mediaPath = e.downloadMedia(evt, "image", ".jpg")
	case msg.GetVideoMessage() != nil:
		mediaType = "video"
		mediaPath = e.downloadMedia(evt, "video", ".mp4")
	case msg.GetAudioMessage() != nil:
		mediaType = "audio"
		mediaPath = e.downloadMedia(evt, "audio", ".ogg")
	case msg.GetDocumentMessage() != nil:
		mediaType = "document"
		mediaPath = e.downloadMedia(evt, "document", ".bin")
	}

	// ── Persist message ───────────────────────────────────────────────────────
	if err := e.DB.SaveMessage(msgID, chatJID, senderJID, text, mediaPath, mediaType, ts); err != nil {
		log.Printf("whatsapp: SaveMessage %q: %v", msgID, err)
	}

	// ── Upsert chat ───────────────────────────────────────────────────────────
	// Use the push name if available; fall back to the JID user part.
	chatName := info.PushName
	if chatName == "" {
		chatName = info.Chat.User
	}
	if err := e.DB.UpsertChat(chatJID, chatName, 1, ts); err != nil {
		log.Printf("whatsapp: UpsertChat %q: %v", chatJID, err)
	}
}

// downloadMedia downloads a media message and writes it to disk.
// mediaType is used as a sub-directory name ("image", "video", "audio", "document").
// ext is the file extension (e.g. ".jpg").
// Returns the saved path, or "" on error.
func (e *Engine) downloadMedia(evt *events.Message, mediaType, ext string) string {
	msg := evt.Message
	msgID := evt.Info.ID

	// Obtain the Downloadable interface for the right message type.
	var downloadable whatsmeow.DownloadableMessage

	switch mediaType {
	case "image":
		if m := msg.GetImageMessage(); m != nil {
			downloadable = m
		}
	case "video":
		if m := msg.GetVideoMessage(); m != nil {
			downloadable = m
		}
	case "audio":
		if m := msg.GetAudioMessage(); m != nil {
			downloadable = m
		}
	case "document":
		if m := msg.GetDocumentMessage(); m != nil {
			downloadable = m
		}
	}

	if downloadable == nil {
		log.Printf("whatsapp: downloadMedia: no downloadable for type %q", mediaType)
		return ""
	}

	data, err := e.Client.Download(context.Background(), downloadable)
	if err != nil {
		log.Printf("whatsapp: download %q (%s): %v", msgID, mediaType, err)
		return ""
	}

	dir := filepath.Join(e.mediaDir, mediaType)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("whatsapp: mkdir %q: %v", dir, err)
		return ""
	}

	savePath := filepath.Join(dir, msgID+ext)
	if err := os.WriteFile(savePath, data, 0o644); err != nil {
		log.Printf("whatsapp: write media %q: %v", savePath, err)
		return ""
	}

	log.Printf("whatsapp: saved %s media to %s", mediaType, savePath)
	return savePath
}

// SendTextMessage sends a plain-text message to the given phone number.
// phone should be in E.164 format without the leading '+' (e.g. "15550001234").
func (e *Engine) SendTextMessage(ctx context.Context, phone, text string) error {
	jidStr := phone + "@s.whatsapp.net"
	jid, err := types.ParseJID(jidStr)
	if err != nil {
		return fmt.Errorf("whatsapp.SendTextMessage: parse JID %q: %w", jidStr, err)
	}

	msg := &waE2E.Message{
		Conversation: proto.String(text),
	}

	_, err = e.Client.SendMessage(ctx, jid, msg)
	if err != nil {
		return fmt.Errorf("whatsapp.SendTextMessage: send to %s: %w", jid, err)
	}
	return nil
}

// IsConnected reports whether the underlying whatsmeow client is connected.
func (e *Engine) IsConnected() bool {
	return e.Client.IsConnected()
}

// WaitConnected blocks until the engine has successfully connected/logged-in
// or the context is cancelled.
func (e *Engine) WaitConnected(ctx context.Context) error {
	select {
	case <-e.Connected:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stop disconnects the client gracefully.
func (e *Engine) Stop() {
	e.Client.Disconnect()
}



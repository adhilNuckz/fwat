package whatsapp

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

// EventUpdate is pushed over SSE to the frontend whenever something changes.
type EventUpdate struct {
	Type    string      `json:"type"` // "qr", "connected", "new_message", "deleted_message", "chat_updated"
	Payload interface{} `json:"payload"`
}

// Engine wraps a whatsmeow client with application-level helpers.
type Engine struct {
	Client     *whatsmeow.Client
	DB         *db.DB
	QRChan     chan []byte
	Connected  chan struct{}
	UpdateChan chan EventUpdate
	mediaDir   string

	connOnce sync.Once
}

// New creates a new Engine.
func New(database *db.DB, mediaDir string) (*Engine, error) {
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
		Client:     client,
		DB:         database,
		QRChan:     make(chan []byte, 10),
		Connected:  make(chan struct{}),
		UpdateChan: make(chan EventUpdate, 100),
		mediaDir:   mediaDir,
	}
	return e, nil
}

// Broadcast sends an event update to all SSE listeners without blocking.
func (e *Engine) Broadcast(up EventUpdate) {
	select {
	case e.UpdateChan <- up:
	default:
	}
}

// Start registers the event handler and connects to WhatsApp.
func (e *Engine) Start() error {
	e.Client.AddEventHandler(e.handleEvent)

	if e.Client.Store.ID == nil {
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
					}
					e.Broadcast(EventUpdate{Type: "qr", Payload: png})
				case "success":
					log.Println("whatsapp: QR scan succeeded, logged in")
					e.connOnce.Do(func() { close(e.Connected) })
					e.Broadcast(EventUpdate{Type: "connected", Payload: "ok"})
				case "timeout":
					log.Println("whatsapp: QR code timed out")
				case "error":
					log.Printf("whatsapp: QR error: %v", evt.Error)
				}
			}
		}()
	} else {
		if err := e.Client.Connect(); err != nil {
			return fmt.Errorf("whatsapp.Start: connect: %w", err)
		}
		e.connOnce.Do(func() { close(e.Connected) })
		e.Broadcast(EventUpdate{Type: "connected", Payload: "ok"})
	}

	return nil
}

// handleEvent is the whatsmeow event dispatcher.
func (e *Engine) handleEvent(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		e.handleMessage(v)
	case *events.HistorySync:
		e.handleHistorySync(v)
	case *events.Connected:
		log.Println("whatsapp: connected")
		e.connOnce.Do(func() { close(e.Connected) })
		e.Broadcast(EventUpdate{Type: "connected", Payload: "ok"})
	case *events.Disconnected:
		log.Println("whatsapp: disconnected")
		e.Broadcast(EventUpdate{Type: "disconnected", Payload: "disconnected"})
	case *events.LoggedOut:
		log.Println("whatsapp: logged out")
		e.Broadcast(EventUpdate{Type: "disconnected", Payload: "logged_out"})
	}
}

// handleHistorySync imports recent chats and messages received upon initial sync.
func (e *Engine) handleHistorySync(evt *events.HistorySync) {
	if evt == nil || evt.Data == nil {
		return
	}
	log.Printf("whatsapp: received history sync type %v", evt.Data.GetSyncType())

	for _, conv := range evt.Data.GetConversations() {
		rawJID := conv.GetID()
		parsedJID, err := types.ParseJID(rawJID)
		if err != nil {
			continue
		}
		chatJID := parsedJID.ToNonAD().String()
		chatName := conv.GetName()
		if chatName == "" {
			chatName = parsedJID.User
		}

		lastMsgTs := time.Now()
		if conv.GetLastMsgTimestamp() > 0 {
			lastMsgTs = time.Unix(int64(conv.GetLastMsgTimestamp()), 0)
		}

		lastMsgPreview := ""
		for _, histMsg := range conv.GetMessages() {
			webMsg := histMsg.GetMessage()
			if webMsg == nil {
				continue
			}
			rawMsg := webMsg.GetMessage()
			if rawMsg == nil {
				continue
			}
			msgKey := webMsg.GetKey()
			if msgKey == nil {
				continue
			}
			msgID := msgKey.GetID()
			isFromMe := msgKey.GetFromMe()

			senderJID := chatJID
			if isFromMe && e.Client.Store.ID != nil {
				senderJID = e.Client.Store.ID.ToNonAD().String()
			} else if msgKey.GetParticipant() != "" {
				senderJID = msgKey.GetParticipant()
			}

			txt := rawMsg.GetConversation()
			if txt == "" {
				if ext := rawMsg.GetExtendedTextMessage(); ext != nil {
					txt = ext.GetText()
				}
			}
			msgTs := time.Unix(int64(webMsg.GetMessageTimestamp()), 0)
			if txt != "" {
				lastMsgPreview = txt
				_ = e.DB.SaveMessage(msgID, chatJID, senderJID, txt, "", "", msgTs, isFromMe)
			}
		}

		_ = e.DB.UpsertChat(chatJID, chatName, int(conv.GetUnreadCount()), lastMsgTs, lastMsgPreview)
	}

	e.Broadcast(EventUpdate{Type: "chat_updated", Payload: "all"})
}

// handleMessage processes an incoming or outgoing message event.
func (e *Engine) handleMessage(evt *events.Message) {
	if evt == nil || evt.Message == nil {
		return
	}

	msg := evt.Message
	info := evt.Info

	// ── Unwrap wrapper containers ────────────────────────────────────────────
	if ephem := msg.GetEphemeralMessage(); ephem != nil && ephem.GetMessage() != nil {
		msg = ephem.GetMessage()
	}
	if vo := msg.GetViewOnceMessage(); vo != nil && vo.GetMessage() != nil {
		msg = vo.GetMessage()
	}
	if vo2 := msg.GetViewOnceMessageV2(); vo2 != nil && vo2.GetMessage() != nil {
		msg = vo2.GetMessage()
	}
	if docCap := msg.GetDocumentWithCaptionMessage(); docCap != nil && docCap.GetMessage() != nil {
		msg = docCap.GetMessage()
	}

	// ── Revoke / delete detection ────────────────────────────────────────────
	if pm := msg.GetProtocolMessage(); pm != nil {
		if pm.GetType() == waE2E.ProtocolMessage_REVOKE {
			deletedID := pm.GetKey().GetID()
			if err := e.DB.MarkDeleted(deletedID); err != nil {
				log.Printf("whatsapp: MarkDeleted %q: %v", deletedID, err)
			}
			e.Broadcast(EventUpdate{Type: "deleted_message", Payload: deletedID})
		}
		return
	}

	// ── Extract text ─────────────────────────────────────────────────────────
	text := msg.GetConversation()
	if text == "" {
		if ext := msg.GetExtendedTextMessage(); ext != nil {
			text = ext.GetText()
		} else if img := msg.GetImageMessage(); img != nil {
			text = img.GetCaption()
		} else if vid := msg.GetVideoMessage(); vid != nil {
			text = vid.GetCaption()
		} else if doc := msg.GetDocumentMessage(); doc != nil {
			text = doc.GetCaption()
		}
	}

	chatJID := info.Chat.ToNonAD().String()
	senderJID := info.Sender.ToNonAD().String()
	msgID := info.ID
	ts := info.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	// ── Determine media type and download ────────────────────────────────────
	mediaPath := ""
	mediaType := ""

	switch {
	case msg.GetImageMessage() != nil:
		mediaType = "image"
		mediaPath = e.downloadMedia(evt, "images", ".jpg")
	case msg.GetVideoMessage() != nil:
		mediaType = "video"
		mediaPath = e.downloadMedia(evt, "videos", ".mp4")
	case msg.GetAudioMessage() != nil:
		mediaType = "audio"
		mediaPath = e.downloadMedia(evt, "audio", ".ogg")
	case msg.GetDocumentMessage() != nil:
		mediaType = "document"
		mediaPath = e.downloadMedia(evt, "documents", ".bin")
	}

	if text == "" && mediaPath == "" {
		return
	}

	isFromMe := info.IsFromMe

	// ── Persist message ───────────────────────────────────────────────────────
	if err := e.DB.SaveMessage(msgID, chatJID, senderJID, text, mediaPath, mediaType, ts, isFromMe); err != nil {
		log.Printf("whatsapp: SaveMessage %q: %v", msgID, err)
	}

	// ── Upsert chat ───────────────────────────────────────────────────────────
	chatName := info.PushName
	if chatName == "" {
		chatName = info.Chat.User
	}
	previewText := text
	if previewText == "" && mediaType != "" {
		previewText = "[" + mediaType + "]"
	}
	unread := 1
	if isFromMe {
		unread = 0
	}
	if err := e.DB.UpsertChat(chatJID, chatName, unread, ts, previewText); err != nil {
		log.Printf("whatsapp: UpsertChat %q: %v", chatJID, err)
	}

	savedMsg := &db.Message{
		ID:          msgID,
		ChatJID:     chatJID,
		SenderJID:   senderJID,
		MessageText: text,
		MediaPath:   mediaPath,
		MediaType:   mediaType,
		IsDeleted:   false,
		Timestamp:   ts,
		IsFromMe:    isFromMe,
	}

	e.Broadcast(EventUpdate{Type: "new_message", Payload: savedMsg})
	e.Broadcast(EventUpdate{Type: "chat_updated", Payload: chatJID})
}

// downloadMedia downloads a media message and writes it to disk.
// Returns the relative URL path, e.g. "images/msgID.jpg".
func (e *Engine) downloadMedia(evt *events.Message, mediaSubdir, ext string) string {
	msg := evt.Message
	msgID := evt.Info.ID

	var downloadable whatsmeow.DownloadableMessage
	switch mediaSubdir {
	case "images":
		if m := msg.GetImageMessage(); m != nil {
			downloadable = m
		}
	case "videos":
		if m := msg.GetVideoMessage(); m != nil {
			downloadable = m
		}
	case "audio":
		if m := msg.GetAudioMessage(); m != nil {
			downloadable = m
		}
	case "documents":
		if m := msg.GetDocumentMessage(); m != nil {
			downloadable = m
		}
	}

	if downloadable == nil {
		return ""
	}

	data, err := e.Client.Download(context.Background(), downloadable)
	if err != nil {
		log.Printf("whatsapp: download %q (%s): %v", msgID, mediaSubdir, err)
		return ""
	}

	dir := filepath.Join(e.mediaDir, mediaSubdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("whatsapp: mkdir %q: %v", dir, err)
		return ""
	}

	filename := msgID + ext
	savePath := filepath.Join(dir, filename)
	if err := os.WriteFile(savePath, data, 0o644); err != nil {
		log.Printf("whatsapp: write media %q: %v", savePath, err)
		return ""
	}

	log.Printf("whatsapp: saved %s media to %s", mediaSubdir, savePath)
	return fmt.Sprintf("%s/%s", mediaSubdir, filename)
}

// SendTextMessage sends a plain-text message to the given phone number or JID.
// It also persists the message in SQLite and broadcasts it over SSE.
func (e *Engine) SendTextMessage(ctx context.Context, phone, text string) (*db.Message, error) {
	cleanPhone := strings.TrimSpace(phone)
	var jid types.JID
	var err error

	if strings.Contains(cleanPhone, "@") {
		jid, err = types.ParseJID(cleanPhone)
		if err != nil {
			return nil, fmt.Errorf("whatsapp.SendTextMessage: parse JID %q: %w", cleanPhone, err)
		}
	} else {
		digitsOnly := ""
		for _, r := range cleanPhone {
			if r >= '0' && r <= '9' {
				digitsOnly += string(r)
			}
		}
		if digitsOnly == "" {
			return nil, fmt.Errorf("invalid phone number: %q", phone)
		}
		jid = types.NewJID(digitsOnly, types.DefaultUserServer)
	}

	msg := &waE2E.Message{
		Conversation: proto.String(text),
	}

	resp, err := e.Client.SendMessage(ctx, jid, msg)
	if err != nil {
		return nil, fmt.Errorf("whatsapp.SendTextMessage: send to %s: %w", jid, err)
	}

	chatJIDStr := jid.ToNonAD().String()
	myJID := ""
	if e.Client.Store.ID != nil {
		myJID = e.Client.Store.ID.ToNonAD().String()
	}
	ts := resp.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}

	_ = e.DB.SaveMessage(resp.ID, chatJIDStr, myJID, text, "", "", ts, true)
	_ = e.DB.UpsertChat(chatJIDStr, cleanPhone, 0, ts, text)

	savedMsg := &db.Message{
		ID:          resp.ID,
		ChatJID:     chatJIDStr,
		SenderJID:   myJID,
		MessageText: text,
		MediaPath:   "",
		MediaType:   "",
		IsDeleted:   false,
		Timestamp:   ts,
		IsFromMe:    true,
	}

	e.Broadcast(EventUpdate{Type: "new_message", Payload: savedMsg})
	e.Broadcast(EventUpdate{Type: "chat_updated", Payload: chatJIDStr})

	return savedMsg, nil
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

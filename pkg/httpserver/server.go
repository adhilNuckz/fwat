package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"embed"

	"whatsapp-dashboard/pkg/auth"
	"whatsapp-dashboard/pkg/db"
	"whatsapp-dashboard/pkg/whatsapp"
)

//go:embed static/*
var staticFiles embed.FS

//go:embed static/index.html
var indexHTML []byte

//go:embed static/help.html
var helpHTML []byte

// Server is the HTTP server for the WhatsApp Web Dashboard.
type Server struct {
	db         *db.DB
	wa         *whatsapp.Engine
	port       string
	sseClients map[chan whatsapp.EventUpdate]struct{}
	sseMu      sync.RWMutex
}

// New creates a new Server instance.
func New(database *db.DB, wa *whatsapp.Engine, port string) *Server {
	return &Server{
		db:         database,
		wa:         wa,
		port:       port,
		sseClients: make(map[chan whatsapp.EventUpdate]struct{}),
	}
}

// Start registers all routes and begins listening for HTTP connections.
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// Serve index.html at root
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})

	// Serve embedded static files
	staticSub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		return fmt.Errorf("failed to create static sub-FS: %w", err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	// API: status
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		jsonOK(w, map[string]interface{}{"connected": s.wa.IsConnected()})
	})

	// API: list chats
	mux.HandleFunc("GET /api/chats", s.handleChats)

	// API: list messages for a JID
	mux.HandleFunc("GET /api/messages", s.handleMessages)

	// API: send a message
	mux.HandleFunc("POST /api/send", s.handleSend)

	// API: ping (send multiple messages)
	mux.HandleFunc("POST /api/ping", s.handlePing)

	// API: schedule a message
	mux.HandleFunc("POST /api/schedule", s.handleSchedule)

	// API: events & QR code SSE stream (both paths work)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/qr", s.handleEvents)

	// Serve media files from ./media/ directory
	mux.Handle("GET /media/", http.StripPrefix("/media/", http.FileServer(http.Dir("./media/"))))

	// Serve help page
	mux.HandleFunc("GET /help", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(helpHTML)
	})

	// Wrap all routes with auth middleware
	handler := auth.Middleware(mux)

	// Broadcast updates to all SSE clients
	go s.distributeEvents()

	// Start the scheduler in the background
	ctx := context.Background()
	go s.runScheduler(ctx)

	addr := "0.0.0.0:" + s.port
	log.Printf("HTTP server listening on %s", addr)
	return http.ListenAndServe(addr, handler)
}

// distributeEvents forwards engine event updates to all active SSE subscribers.
func (s *Server) distributeEvents() {
	for up := range s.wa.UpdateChan {
		s.sseMu.RLock()
		for ch := range s.sseClients {
			select {
			case ch <- up:
			default:
			}
		}
		s.sseMu.RUnlock()
	}
}

// handleChats returns a JSON list of all chats from the database.
func (s *Server) handleChats(w http.ResponseWriter, r *http.Request) {
	chats, err := s.db.GetChats()
	if err != nil {
		jsonErr(w, "failed to fetch chats: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if chats == nil {
		chats = []db.Chat{}
	}
	jsonOK(w, chats)
}

// handleMessages returns a JSON list of messages for a given JID query parameter.
func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	jid := r.URL.Query().Get("jid")
	if jid == "" {
		jsonErr(w, "missing jid query parameter", http.StatusBadRequest)
		return
	}
	messages, err := s.db.GetMessages(jid)
	if err != nil {
		jsonErr(w, "failed to fetch messages: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if messages == nil {
		messages = []db.Message{}
	}
	jsonOK(w, messages)
}

// handleSend sends a text message via the WhatsApp engine.
func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone string `json:"phone"`
		Text  string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Phone == "" || body.Text == "" {
		jsonErr(w, "phone and text are required", http.StatusBadRequest)
		return
	}
	msg, err := s.wa.SendTextMessage(r.Context(), body.Phone, body.Text)
	if err != nil {
		jsonErr(w, "failed to send message: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]interface{}{"status": "sent", "message": msg})
}

// handlePing sends multiple messages with a delay between each.
func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone           string `json:"phone"`
		Text            string `json:"text"`
		Count           int    `json:"count"`
		IntervalSeconds int    `json:"interval_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Phone == "" || body.Text == "" {
		jsonErr(w, "phone and text are required", http.StatusBadRequest)
		return
	}
	if body.Count <= 0 {
		body.Count = 1
	}
	if body.IntervalSeconds < 0 {
		body.IntervalSeconds = 0
	}

	go func() {
		ctx := context.Background()
		for i := 0; i < body.Count; i++ {
			if i > 0 && body.IntervalSeconds > 0 {
				time.Sleep(time.Duration(body.IntervalSeconds) * time.Second)
			}
			msg := body.Text + " (" + strconv.Itoa(i+1) + "/" + strconv.Itoa(body.Count) + ")"
			if _, err := s.wa.SendTextMessage(ctx, body.Phone, msg); err != nil {
				log.Printf("ping send error (attempt %d/%d): %v", i+1, body.Count, err)
			}
		}
	}()

	jsonOK(w, map[string]string{"status": "ping started", "count": strconv.Itoa(body.Count)})
}

// handleSchedule saves a scheduled message to the database.
func (s *Server) handleSchedule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Phone        string `json:"phone"`
		Text         string `json:"text"`
		ScheduleTime string `json:"schedule_time"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonErr(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Phone == "" || body.Text == "" || body.ScheduleTime == "" {
		jsonErr(w, "phone, text, and schedule_time are required", http.StatusBadRequest)
		return
	}
	scheduleTime, err := time.Parse(time.RFC3339, body.ScheduleTime)
	if err != nil {
		jsonErr(w, "invalid schedule_time, expected RFC3339 format: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.db.SaveScheduledMessage(body.Phone, body.Text, scheduleTime); err != nil {
		jsonErr(w, "failed to save scheduled message: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w, map[string]string{"status": "scheduled"})
}

// handleEvents streams SSE real-time updates (QR, connection, new messages, deletions).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	clientChan := make(chan whatsapp.EventUpdate, 50)
	s.sseMu.Lock()
	s.sseClients[clientChan] = struct{}{}
	s.sseMu.Unlock()

	defer func() {
		s.sseMu.Lock()
		delete(s.sseClients, clientChan)
		s.sseMu.Unlock()
	}()

	// Send initial status
	if s.wa.IsConnected() {
		fmt.Fprintf(w, "event: connected\ndata: ok\n\n")
	} else {
		fmt.Fprintf(w, "event: status\ndata: {\"connected\":false}\n\n")
	}
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case qrPNG := <-s.wa.QRChan:
			encoded := base64.StdEncoding.EncodeToString(qrPNG)
			fmt.Fprintf(w, "event: qr\ndata: %s\n\n", encoded)
			flusher.Flush()
		case up := <-clientChan:
			switch up.Type {
			case "qr":
				if png, ok := up.Payload.([]byte); ok {
					encoded := base64.StdEncoding.EncodeToString(png)
					fmt.Fprintf(w, "event: qr\ndata: %s\n\n", encoded)
				}
			case "connected":
				fmt.Fprintf(w, "event: connected\ndata: ok\n\n")
			case "disconnected":
				fmt.Fprintf(w, "event: disconnected\ndata: disconnected\n\n")
			case "new_message":
				data, _ := json.Marshal(up.Payload)
				fmt.Fprintf(w, "event: new_message\ndata: %s\n\n", string(data))
			case "deleted_message":
				data, _ := json.Marshal(map[string]interface{}{"id": up.Payload})
				fmt.Fprintf(w, "event: deleted_message\ndata: %s\n\n", string(data))
			case "chat_updated":
				data, _ := json.Marshal(map[string]interface{}{"jid": up.Payload})
				fmt.Fprintf(w, "event: chat_updated\ndata: %s\n\n", string(data))
			}
			flusher.Flush()
		case <-ticker.C:
			// Heartbeat
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// runScheduler polls the database every 30 seconds for pending scheduled messages.
func (s *Server) runScheduler(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			items, err := s.db.GetPendingScheduled()
			if err != nil {
				log.Println("scheduler error:", err)
				continue
			}
			for _, item := range items {
				if _, err := s.wa.SendTextMessage(ctx, item.TargetPhone, item.MessageText); err != nil {
					log.Printf("scheduled send error to %s: %v", item.TargetPhone, err)
				} else {
					s.db.MarkScheduledSent(item.ID)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// jsonOK writes v as a JSON response with HTTP 200.
func jsonOK(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// jsonErr writes an error message as a JSON response with the given HTTP status code.
func jsonErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

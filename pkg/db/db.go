package db

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// DB wraps a *sql.DB with application-level methods.
type DB struct {
	conn *sql.DB
}

// Chat represents a row in the chats table.
type Chat struct {
	JID           string    `json:"jid"`
	Name          string    `json:"name"`
	UnreadCount   int       `json:"unread_count"`
	LastMessageAt time.Time `json:"last_message_at"`
}

// Message represents a row in the messages table.
type Message struct {
	ID          string    `json:"id"`
	ChatJID     string    `json:"chat_jid"`
	SenderJID   string    `json:"sender_jid"`
	MessageText string    `json:"message_text"`
	MediaPath   string    `json:"media_path"`
	MediaType   string    `json:"media_type"`
	IsDeleted   bool      `json:"is_deleted"`
	Timestamp   time.Time `json:"timestamp"`
}

// ScheduledMessage represents a row in the scheduled_messages table.
type ScheduledMessage struct {
	ID           int64     `json:"id"`
	TargetPhone  string    `json:"target_phone"`
	MessageText  string    `json:"message_text"`
	ScheduleTime time.Time `json:"schedule_time"`
	Status       string    `json:"status"`
}

// New opens (or creates) the SQLite database at path and initialises the schema.
func New(path string) (*DB, error) {
	conn, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("db.New: open %q: %w", path, err)
	}
	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("db.New: ping: %w", err)
	}
	d := &DB{conn: conn}
	if err := d.InitSchema(); err != nil {
		return nil, fmt.Errorf("db.New: init schema: %w", err)
	}
	log.Printf("database opened: %s", path)
	return d, nil
}

// InitSchema creates the required tables if they do not already exist.
func (d *DB) InitSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS chats (
			jid              TEXT PRIMARY KEY,
			name             TEXT,
			unread_count     INTEGER   DEFAULT 0,
			last_message_at  TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS messages (
			id           TEXT PRIMARY KEY,
			chat_jid     TEXT,
			sender_jid   TEXT,
			message_text TEXT,
			media_path   TEXT,
			media_type   TEXT,
			is_deleted   BOOLEAN   DEFAULT FALSE,
			timestamp    TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS scheduled_messages (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			target_phone  TEXT,
			message_text  TEXT,
			schedule_time TIMESTAMP,
			status        TEXT DEFAULT 'pending'
		);`,
	}
	for _, s := range stmts {
		if _, err := d.conn.Exec(s); err != nil {
			return fmt.Errorf("InitSchema: %w", err)
		}
	}
	return nil
}

// UpsertChat inserts or updates a chat record.
func (d *DB) UpsertChat(jid, name string, unreadCount int, lastMsgAt time.Time) error {
	_, err := d.conn.Exec(
		`INSERT INTO chats (jid, name, unread_count, last_message_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(jid) DO UPDATE SET
		     name             = excluded.name,
		     unread_count     = excluded.unread_count,
		     last_message_at  = excluded.last_message_at`,
		jid, name, unreadCount, lastMsgAt,
	)
	if err != nil {
		return fmt.Errorf("UpsertChat: %w", err)
	}
	return nil
}

// SaveMessage inserts a message record. Duplicate IDs are ignored.
func (d *DB) SaveMessage(id, chatJID, senderJID, text, mediaPath, mediaType string, ts time.Time) error {
	_, err := d.conn.Exec(
		`INSERT OR IGNORE INTO messages
		     (id, chat_jid, sender_jid, message_text, media_path, media_type, is_deleted, timestamp)
		 VALUES (?, ?, ?, ?, ?, ?, FALSE, ?)`,
		id, chatJID, senderJID, text, mediaPath, mediaType, ts,
	)
	if err != nil {
		return fmt.Errorf("SaveMessage: %w", err)
	}
	return nil
}

// MarkDeleted sets is_deleted = TRUE for the given message ID.
func (d *DB) MarkDeleted(msgID string) error {
	_, err := d.conn.Exec(
		`UPDATE messages SET is_deleted = TRUE WHERE id = ?`, msgID,
	)
	if err != nil {
		return fmt.Errorf("MarkDeleted: %w", err)
	}
	return nil
}

// GetChats returns all chats ordered by last_message_at descending.
func (d *DB) GetChats() ([]Chat, error) {
	rows, err := d.conn.Query(
		`SELECT jid, name, unread_count, last_message_at
		 FROM chats
		 ORDER BY last_message_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("GetChats: %w", err)
	}
	defer rows.Close()

	var chats []Chat
	for rows.Next() {
		var c Chat
		if err := rows.Scan(&c.JID, &c.Name, &c.UnreadCount, &c.LastMessageAt); err != nil {
			return nil, fmt.Errorf("GetChats scan: %w", err)
		}
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

// GetMessages returns all messages for a given chat JID ordered by timestamp ascending.
func (d *DB) GetMessages(chatJID string) ([]Message, error) {
	rows, err := d.conn.Query(
		`SELECT id, chat_jid, sender_jid, message_text, media_path, media_type, is_deleted, timestamp
		 FROM messages
		 WHERE chat_jid = ?
		 ORDER BY timestamp ASC`,
		chatJID,
	)
	if err != nil {
		return nil, fmt.Errorf("GetMessages: %w", err)
	}
	defer rows.Close()

	var msgs []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(
			&m.ID, &m.ChatJID, &m.SenderJID, &m.MessageText,
			&m.MediaPath, &m.MediaType, &m.IsDeleted, &m.Timestamp,
		); err != nil {
			return nil, fmt.Errorf("GetMessages scan: %w", err)
		}
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.conn.Close()
}

// SaveScheduledMessage is an alias for SaveScheduled (used by httpserver).
func (d *DB) SaveScheduledMessage(phone, text string, scheduleAt time.Time) error {
	return d.SaveScheduled(phone, text, scheduleAt)
}

// SaveScheduled inserts a new scheduled message with status 'pending'.
func (d *DB) SaveScheduled(phone, text string, scheduleAt time.Time) error {
	_, err := d.conn.Exec(
		`INSERT INTO scheduled_messages (target_phone, message_text, schedule_time, status)
		 VALUES (?, ?, ?, 'pending')`,
		phone, text, scheduleAt,
	)
	if err != nil {
		return fmt.Errorf("SaveScheduled: %w", err)
	}
	return nil
}

// GetPendingScheduled returns scheduled messages that are pending and due now or in the past.
func (d *DB) GetPendingScheduled() ([]ScheduledMessage, error) {
	rows, err := d.conn.Query(
		`SELECT id, target_phone, message_text, schedule_time, status
		 FROM scheduled_messages
		 WHERE status = 'pending' AND schedule_time <= ?`,
		time.Now(),
	)
	if err != nil {
		return nil, fmt.Errorf("GetPendingScheduled: %w", err)
	}
	defer rows.Close()

	var items []ScheduledMessage
	for rows.Next() {
		var s ScheduledMessage
		if err := rows.Scan(&s.ID, &s.TargetPhone, &s.MessageText, &s.ScheduleTime, &s.Status); err != nil {
			return nil, fmt.Errorf("GetPendingScheduled scan: %w", err)
		}
		items = append(items, s)
	}
	return items, rows.Err()
}

// MarkScheduledSent updates the status of a scheduled message to 'sent'.
func (d *DB) MarkScheduledSent(id int64) error {
	_, err := d.conn.Exec(
		`UPDATE scheduled_messages SET status = 'sent' WHERE id = ?`, id,
	)
	if err != nil {
		return fmt.Errorf("MarkScheduledSent: %w", err)
	}
	return nil
}

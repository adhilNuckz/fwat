package main

import (
	"log"
	"os"
	"path/filepath"

	"whatsapp-dashboard/pkg/db"
	"whatsapp-dashboard/pkg/httpserver"
	"whatsapp-dashboard/pkg/whatsapp"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("Starting WhatsApp Web Dashboard...")

	// Determine working directory
	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get working directory: %v", err)
	}

	// Media directory
	mediaDir := filepath.Join(wd, "media")
	dbDir := filepath.Join(wd, "data", "db")

	// Ensure directories exist
	for _, dir := range []string{
		filepath.Join(mediaDir, "images"),
		filepath.Join(mediaDir, "videos"),
		filepath.Join(mediaDir, "audio"),
		filepath.Join(mediaDir, "documents"),
		dbDir,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Fatalf("Failed to create directory %s: %v", dir, err)
		}
	}

	// Initialize database
	dbPath := filepath.Join(dbDir, "dashboard.db")
	database, err := db.New(dbPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()
	log.Printf("Database initialized at %s", dbPath)

	// Initialize WhatsApp engine
	waEngine, err := whatsapp.New(database, mediaDir)
	if err != nil {
		log.Fatalf("Failed to initialize WhatsApp engine: %v", err)
	}

	// Start WhatsApp engine in background
	go func() {
		if err := waEngine.Start(); err != nil {
			log.Printf("WhatsApp engine error: %v", err)
		}
	}()

	// Determine port
	port := os.Getenv("PORT")
	if port == "" {
		port = "5050"
	}

	// Start HTTP server
	srv := httpserver.New(database, waEngine, port)
	log.Printf("Dashboard listening on http://0.0.0.0:%s", port)
	if err := srv.Start(); err != nil {
		log.Fatalf("HTTP server error: %v", err)
	}
}

# WhatsApp Self-Hosted Web Dashboard & Automation Engine

[![Go Version](https://img.shields.io/badge/Go-1.21%2B-00ADD8?style=flat&logo=go)](https://go.dev/)
[![Platform](https://img.shields.io/badge/Platform-Ubuntu%20Linux-E95420?style=flat&logo=ubuntu)](https://ubuntu.com/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A self-hosted, lightweight WhatsApp Web Dashboard and Automation Engine built in **Go (Golang)** using `go.mau.fi/whatsmeow` and **SQLite**. Designed to run effortlessly on low-spec VPS servers (even a **1GB RAM** Ubuntu instance) with zero crashes, auto-swap protection, and persistent background systemd service.

---

## ⚡ Zero-Touch One-Line Installation

Run this single command on your fresh Ubuntu VPS (as `root` or with `sudo`):

```bash
curl -fsSL https://raw.githubusercontent.com/adhilNuckz/fwat/main/deploy.sh | sudo bash
```

### What this one-liner does automatically:
1. **System & Toolchain:** Updates APT packages, installs `gcc`, `sqlite3`, `libsqlite3-dev`, and official **Go 1.22+**.
2. **OOM Protection:** Creates, formats, and activates a **2GB swapfile** (`/swapfile`) with automatic `/etc/fstab` persistence to protect 1GB RAM instances.
3. **Repository Setup:** Automatically clones/updates the latest codebase into `/opt/fwat`.
4. **Compile:** Builds the optimized standalone binary `whatsapp-dashboard` with CGO enabled.
5. **Systemd Service:** Configures, enables on boot, and starts `/etc/systemd/system/wa-dashboard.service`.
6. **Outputs Live Details:** Prints your VPS IP URL and login credentials.

---

## 🔑 Default Credentials

- **URL:** `http://<YOUR-VPS-IP>:5050`
- **Username:** `admin`
- **Password:** `adminpassword123`

> [!TIP]
> To customize credentials, edit `/etc/systemd/system/wa-dashboard.service`:
> ```ini
> Environment=AUTH_USERNAME=your_username
> Environment=AUTH_PASSWORD=your_secure_password
> ```
> Then reload:
> ```bash
> sudo systemctl daemon-reload && sudo systemctl restart wa-dashboard
> ```

---

## 📱 Features

- **Web Dashboard (Port 5050):** Responsive dark-mode interface built with Tailwind CSS and Vanilla JS/HTMX, fully embedded into the Go binary.
- **Live QR Streaming:** Pair your WhatsApp account in real-time via Server-Sent Events (SSE).
- **Auto Media Saving:** Downloads all incoming images, videos, audio/voice notes, and documents instantly to disk under `./media/`.
- **Anti-Delete / Revoke Interceptor:** When someone deletes a message ("Delete for Everyone"), the message is flagged with a red `[DELETED MESSAGE]` badge, while the original text and media are preserved intact.
- **⚡ Ping Contact Tool:** Mass messaging tool with configurable repeat count and delay intervals.
- **📅 Scheduled Messaging:** Schedule messages to any contact with background cron worker polling every 30s.
- **➕ Direct New Chat:** Start conversations with any phone number without adding them as a contact first.
- **📖 Built-in Help Center (`/help`):** Comprehensive command center for VPS management, troubleshooting, and logs.

---

## 🛠️ Service Management Commands

```bash
# Check service status
sudo systemctl status wa-dashboard

# View real-time logs
sudo journalctl -u wa-dashboard -f

# Restart the dashboard engine
sudo systemctl restart wa-dashboard

# Stop the dashboard
sudo systemctl stop wa-dashboard
```

---

## 📁 Storage & Directory Layout

```
/opt/fwat/
├── whatsapp-dashboard               # Compiled standalone binary
├── data/
│   └── db/
│       ├── dashboard.db             # Application database (chats, messages, schedules)
│       └── whatsmeow.db             # WhatsApp encrypted session store
└── media/
    ├── images/                      # Saved JPEG images (.jpg)
    ├── videos/                      # Saved MP4 videos (.mp4)
    ├── audio/                       # Saved audio & voice notes (.ogg)
    └── documents/                   # Downloaded documents (.bin)
```

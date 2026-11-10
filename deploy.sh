#!/usr/bin/env bash
# =============================================================================
# deploy.sh — Zero-touch WhatsApp Dashboard Setup for Ubuntu VPS
# =============================================================================
set -euo pipefail

# ─── Colors ────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m' # No Color

STEP=0
step() {
    STEP=$((STEP+1))
    echo -e "\n${CYAN}${BOLD}[Step ${STEP}]${NC} ${BOLD}${1}${NC}"
}
ok()   { echo -e "  ${GREEN}✓${NC} $1"; }
warn() { echo -e "  ${YELLOW}⚠${NC}  $1"; }
err()  { echo -e "  ${RED}✗${NC} $1"; exit 1; }

echo -e "${BOLD}${GREEN}"
echo "  ╔══════════════════════════════════════════════════════╗"
echo "  ║       WhatsApp Dashboard — Automated Installer       ║"
echo "  ╚══════════════════════════════════════════════════════╝"
echo -e "${NC}"

# ─── Validate Running as Root ───────────────────────────────────────────────
if [ "$EUID" -ne 0 ]; then
    err "Please run as root: sudo ./deploy.sh"
fi

# ─── Detect Repo Root ───────────────────────────────────────────────────────
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_NAME="$(basename "$REPO_DIR")"
BINARY_NAME="whatsapp-dashboard"
SERVICE_NAME="wa-dashboard"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"

ok "Repository root: ${REPO_DIR}"

# ═══════════════════════════════════════════════════════════════════════════
# STEP 1: System Update & Dependencies
# ═══════════════════════════════════════════════════════════════════════════
step "Updating system packages"
export DEBIAN_FRONTEND=noninteractive
apt update -y && apt upgrade -y -o Dpkg::Options::="--force-confdef" -o Dpkg::Options::="--force-confold"
ok "System updated"

step "Installing required packages"
apt install -y \
    golang \
    git \
    sqlite3 \
    libsqlite3-dev \
    gcc \
    curl \
    ca-certificates \
    build-essential
ok "Build tools installed"

# Verify Go version
GO_VERSION=$(go version | awk '{print $3}' | sed 's/go//')
ok "Go version: ${GO_VERSION}"

# ═══════════════════════════════════════════════════════════════════════════
# STEP 2: Swap File (Memory Guard for 1GB VPS)
# ═══════════════════════════════════════════════════════════════════════════
step "Configuring swap memory"
SWAP_FILE="/swapfile"
SWAP_SIZE="2G"

if [ -f "$SWAP_FILE" ]; then
    warn "Swap file ${SWAP_FILE} already exists — skipping creation"
else
    echo "  Allocating ${SWAP_SIZE} swap file at ${SWAP_FILE}..."
    fallocate -l "$SWAP_SIZE" "$SWAP_FILE" || dd if=/dev/zero of="$SWAP_FILE" bs=1M count=2048 status=progress
    chmod 600 "$SWAP_FILE"
    mkswap "$SWAP_FILE"
    swapon "$SWAP_FILE"

    # Persist across reboots
    if ! grep -q "$SWAP_FILE" /etc/fstab; then
        echo "${SWAP_FILE} none swap sw 0 0" >> /etc/fstab
        ok "Swap persisted in /etc/fstab"
    fi
    ok "2GB swap file created and activated"
fi

# Show swap status
swapon --show
ok "Swap configured (protects against OOM on 1GB RAM)"

# ═══════════════════════════════════════════════════════════════════════════
# STEP 3: Directory Structure
# ═══════════════════════════════════════════════════════════════════════════
step "Creating data directory structure"
mkdir -p "${REPO_DIR}/data/db"
mkdir -p "${REPO_DIR}/media/images"
mkdir -p "${REPO_DIR}/media/videos"
mkdir -p "${REPO_DIR}/media/audio"
mkdir -p "${REPO_DIR}/media/documents"
chmod -R 755 "${REPO_DIR}/data" "${REPO_DIR}/media"
ok "Directory structure created:"
find "${REPO_DIR}/data" "${REPO_DIR}/media" -type d | sort | sed 's|'"${REPO_DIR}"'|.|g' | sed 's/^/    /'

# ═══════════════════════════════════════════════════════════════════════════
# STEP 4: Go Build
# ═══════════════════════════════════════════════════════════════════════════
step "Downloading Go modules"
cd "${REPO_DIR}"
go mod download
ok "Go modules downloaded"

step "Compiling application binary (CGO enabled for SQLite)"
CGO_ENABLED=1 go build \
    -o "${REPO_DIR}/${BINARY_NAME}" \
    -ldflags="-s -w" \
    ./cmd/main.go
ok "Binary compiled: ${REPO_DIR}/${BINARY_NAME}"
ls -lh "${REPO_DIR}/${BINARY_NAME}"

# ═══════════════════════════════════════════════════════════════════════════
# STEP 5: Systemd Service
# ═══════════════════════════════════════════════════════════════════════════
step "Configuring systemd service"

cat > "$SERVICE_FILE" <<EOF
[Unit]
Description=WhatsApp Web Dashboard Engine
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=${REPO_DIR}
ExecStart=${REPO_DIR}/${BINARY_NAME}
Restart=always
RestartSec=5s
StartLimitIntervalSec=0
StandardOutput=journal
StandardError=journal

# Authentication (change these to customize credentials)
Environment=AUTH_USERNAME=admin
Environment=AUTH_PASSWORD=adminpassword123
Environment=PORT=5050

[Install]
WantedBy=multi-user.target
EOF

ok "Service file written to ${SERVICE_FILE}"

systemctl daemon-reload
ok "Systemd daemon reloaded"

# Stop existing instance if running
if systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null; then
    warn "Stopping existing ${SERVICE_NAME} instance..."
    systemctl stop "${SERVICE_NAME}"
fi

systemctl enable "${SERVICE_NAME}"
ok "Service enabled on boot"

systemctl start "${SERVICE_NAME}"
ok "Service started"

# Wait a moment for it to stabilize
sleep 3

# ═══════════════════════════════════════════════════════════════════════════
# STEP 6: Completion Summary
# ═══════════════════════════════════════════════════════════════════════════
# Detect public IP
VPS_IP=$(curl -s --max-time 5 ifconfig.me 2>/dev/null || \
         curl -s --max-time 5 ipinfo.io/ip 2>/dev/null || \
         hostname -I | awk '{print $1}')

echo ""
echo -e "${GREEN}${BOLD}╔══════════════════════════════════════════════════════════╗${NC}"
echo -e "${GREEN}${BOLD}║           ✅  DEPLOYMENT COMPLETE!                       ║${NC}"
echo -e "${GREEN}${BOLD}╚══════════════════════════════════════════════════════════╝${NC}"
echo ""
echo -e "  ${BOLD}Service Status:${NC}"
systemctl status "${SERVICE_NAME}" --no-pager --lines=5 | sed 's/^/    /'
echo ""
echo -e "  ${BOLD}${CYAN}Live Dashboard URL:${NC}"
echo -e "    ${BLUE}http://${VPS_IP}:5050${NC}"
echo ""
echo -e "  ${BOLD}Default Credentials:${NC}"
echo -e "    User: ${YELLOW}admin${NC}"
echo -e "    Pass: ${YELLOW}adminpassword123${NC}"
echo ""
echo -e "  ${BOLD}Useful Commands:${NC}"
echo -e "    ${CYAN}sudo systemctl status ${SERVICE_NAME}${NC}       — Check status"
echo -e "    ${CYAN}sudo journalctl -u ${SERVICE_NAME} -f${NC}        — Live logs"
echo -e "    ${CYAN}sudo systemctl restart ${SERVICE_NAME}${NC}       — Restart"
echo ""
echo -e "  ${BOLD}Next Steps:${NC}"
echo -e "    1. Open the dashboard URL above in your browser"
echo -e "    2. Log in with the credentials shown"
echo -e "    3. Scan the QR code with WhatsApp → Linked Devices"
echo ""
echo -e "  ${YELLOW}${BOLD}⚠  To change credentials, edit:${NC}"
echo -e "    ${SERVICE_FILE}"
echo -e "    Then run: ${CYAN}sudo systemctl daemon-reload && sudo systemctl restart ${SERVICE_NAME}${NC}"
echo ""

#!/bin/sh
# k8sclaw installer
# Usage: curl -fsSL https://deploy.k8sclaw.ai/install.sh | sh
#
# Downloads the latest k8sclaw CLI release from GitHub and installs
# the binary to /usr/local/bin (or ~/.local/bin if no sudo).

set -e

REPO="AlexsJones/k8sclaw"
BINARY="k8sclaw"

# --- helpers ---

info() { printf '  \033[1;34m>\033[0m %s\n' "$*"; }
err()  { printf '  \033[1;31m!\033[0m %s\n' "$*" >&2; exit 1; }

need() {
    command -v "$1" >/dev/null 2>&1 \
        || err "Required tool '$1' not found. Please install it and try again."
}

# --- detect platform ---

detect_platform() {
    OS="$(uname -s)"
    ARCH="$(uname -m)"

    case "$OS" in
        Linux)  OS="linux" ;;
        Darwin) OS="darwin" ;;
        *)      err "Unsupported OS: $OS" ;;
    esac

    case "$ARCH" in
        x86_64|amd64)   ARCH="amd64" ;;
        aarch64|arm64)  ARCH="arm64" ;;
        *)              err "Unsupported architecture: $ARCH" ;;
    esac

    PLATFORM="${OS}-${ARCH}"
}

# --- fetch latest release ---

fetch_latest_tag() {
    need curl

    # Use the releases redirect instead of the API to avoid
    # GitHub's 60-request/hour rate limit on unauthenticated API calls.
    TAG="$(curl -fsSI "https://github.com/${REPO}/releases/latest" 2>/dev/null \
        | grep -i '^location:' \
        | head -1 \
        | sed 's|.*/tag/||' \
        | tr -d '\r\n')"

    [ -n "$TAG" ] || err "Could not determine latest release. Check https://github.com/${REPO}/releases"
}

# --- download and install ---

install() {
    ASSET="${BINARY}-${PLATFORM}.tar.gz"
    URL="https://github.com/${REPO}/releases/download/${TAG}/${ASSET}"

    TMPDIR="$(mktemp -d)"
    trap 'rm -rf "$TMPDIR"' EXIT

    info "Downloading ${BINARY} ${TAG} for ${PLATFORM}..."
    curl -fsSL "$URL" -o "${TMPDIR}/${ASSET}" \
        || err "Download failed. Asset '${ASSET}' may not exist for your platform.\n  Check: https://github.com/${REPO}/releases/tag/${TAG}"

    info "Extracting..."
    tar -xzf "${TMPDIR}/${ASSET}" -C "$TMPDIR"

    BIN="$(find "$TMPDIR" -name "$BINARY" -type f | head -1)"
    [ -n "$BIN" ] || err "Binary not found in archive."

    chmod +x "$BIN"

    # Install to /usr/local/bin or fall back to ~/.local/bin
    if [ -w /usr/local/bin ]; then
        INSTALL_DIR="/usr/local/bin"
    elif command -v sudo >/dev/null 2>&1; then
        info "Installing to /usr/local/bin (requires sudo)..."
        INSTALL_DIR="/usr/local/bin"
        sudo mv "$BIN" "${INSTALL_DIR}/${BINARY}"
        info "Installed ${BINARY} to ${INSTALL_DIR}/${BINARY}"
        return
    else
        INSTALL_DIR="${HOME}/.local/bin"
        mkdir -p "$INSTALL_DIR"
    fi

    mv "$BIN" "${INSTALL_DIR}/${BINARY}"
    info "Installed ${BINARY} to ${INSTALL_DIR}/${BINARY}"

    # Check if install dir is in PATH
    case ":$PATH:" in
        *":${INSTALL_DIR}:"*) ;;
        *) info "Add ${INSTALL_DIR} to your PATH to use '${BINARY}' directly." ;;
    esac
}

# --- main ---

main() {
    info "k8sclaw installer"
    detect_platform
    fetch_latest_tag
    install
    info "Done! Run 'k8sclaw install' to deploy K8sClaw to your cluster."
}

main

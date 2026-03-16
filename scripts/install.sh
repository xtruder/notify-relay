#!/usr/bin/env bash
set -euo pipefail

REPO="xtruder/notify-relay"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
SYSTEMD_DIR="${SYSTEMD_DIR:-$HOME/.config/systemd/user}"
VERSION="${VERSION:-latest}"

usage() {
  cat <<'EOF'
Usage: install.sh [options]

Install notify-relay binaries from GitHub releases.

Options:
  --version <tag>         Release tag to install, default: latest
  --install-dir <dir>     Binary install directory, default: ~/.local/bin
  --systemd-dir <dir>     User systemd directory, default: ~/.config/systemd/user
  --no-systemd            Skip installing the systemd user service
  -h, --help              Show this help

Environment:
  INSTALL_DIR             Same as --install-dir
  SYSTEMD_DIR             Same as --systemd-dir
  VERSION                 Same as --version
EOF
}

need() {
  command -v "$1" >/dev/null 2>&1 || {
    printf 'missing required command: %s\n' "$1" >&2
    exit 1
  }
}

resolve_version() {
  if [[ "$VERSION" != "latest" ]]; then
    printf '%s' "$VERSION"
    return
  fi
  local final_url
  final_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "https://github.com/${REPO}/releases/latest")"
  basename "$final_url"
}

platform() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$os" in
    linux|darwin) ;;
    *) printf 'unsupported OS: %s\n' "$os" >&2; exit 1 ;;
  esac
  case "$arch" in
    x86_64|amd64) arch="amd64" ;;
    arm64|aarch64) arch="arm64" ;;
    *) printf 'unsupported architecture: %s\n' "$arch" >&2; exit 1 ;;
  esac
  printf '%s %s' "$os" "$arch"
}

download_release() {
  local version="$1" os="$2" arch="$3" tmpdir="$4"
  local clean_version asset base
  clean_version="${version#v}"
  asset="notify-relay_${clean_version}_${os}_${arch}.tar.gz"
  base="https://github.com/${REPO}/releases/download/${version}"
  curl -fsSL "${base}/${asset}" -o "${tmpdir}/${asset}"
  tar -xzf "${tmpdir}/${asset}" -C "$tmpdir"
  find "$tmpdir" -mindepth 1 -maxdepth 1 -type d -name 'notify-relay_*' | head -n 1
}

install_files() {
  local release_dir="$1" install_systemd="$2"
  mkdir -p "$INSTALL_DIR"
  install -m755 "$release_dir/notify-relayd" "$INSTALL_DIR/notify-relayd"
  install -m755 "$release_dir/notify-send-proxy" "$INSTALL_DIR/notify-send-proxy"
  if [[ "$install_systemd" == "1" ]]; then
    mkdir -p "$SYSTEMD_DIR"
    install -m644 "$release_dir/packaging/systemd/notify-relayd.service" "$SYSTEMD_DIR/notify-relayd.service"
  fi
}

main() {
  need curl
  need tar
  need install

  local install_systemd=1
  local detected_os

  detected_os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  if [[ "$detected_os" == "darwin" ]]; then
    install_systemd=0
  fi

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version)
        VERSION="$2"
        shift 2
        ;;
      --install-dir)
        INSTALL_DIR="$2"
        shift 2
        ;;
      --systemd-dir)
        SYSTEMD_DIR="$2"
        shift 2
        ;;
      --no-systemd)
        install_systemd=0
        shift
        ;;
      -h|--help)
        usage
        exit 0
        ;;
      *)
        printf 'unknown option: %s\n' "$1" >&2
        usage >&2
        exit 1
        ;;
    esac
  done

  read -r os arch < <(platform)
  local version tmpdir release_dir
  version="$(resolve_version)"
  tmpdir="$(mktemp -d)"
  trap 'rm -rf "$tmpdir"' EXIT

  printf 'Installing %s for %s/%s into %s\n' "$version" "$os" "$arch" "$INSTALL_DIR"
  release_dir="$(download_release "$version" "$os" "$arch" "$tmpdir")"
  install_files "$release_dir" "$install_systemd"

  cat <<EOF
Installed:
  $INSTALL_DIR/notify-relayd
  $INSTALL_DIR/notify-send-proxy
EOF

  cat <<EOF

Optional symlink if you want this proxy to replace notify-send:
  ln -sfn "$INSTALL_DIR/notify-send-proxy" "$INSTALL_DIR/notify-send"
EOF

  if [[ "$install_systemd" == "1" ]]; then
    cat <<EOF

Systemd unit installed to:
  $SYSTEMD_DIR/notify-relayd.service

Next steps:
  systemctl --user daemon-reload
  systemctl --user enable --now notify-relayd.service
EOF
  fi

  if [[ "$detected_os" == "darwin" ]]; then
    printf '\nNote: macOS is supported for the client proxy, but notify-relayd is intended for Linux hosts.\n'
  fi
}

main "$@"

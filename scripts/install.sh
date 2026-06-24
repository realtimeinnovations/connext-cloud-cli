#!/usr/bin/env sh

# Copyright (c) 2026 Real-Time Innovations, Inc.  All rights reserved.
# No duplications, whole or partial, manual or electronic, may be made
# without express written permission.  Any such copies, or revisions thereof,
# must display this notice unaltered.
# This code contains trade secrets of Real-Time Innovations, Inc.

set -eu

OWNER="realtimeinnovations"
REPO="connext-cloud-cli"
BINARY="rticloud"
INSTALL_DIR="${INSTALL_DIR:-/usr/local/bin}"
VERSION="${VERSION:-latest}"

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

detect_os() {
  case "$(uname -s)" in
    Darwin) echo "darwin" ;;
    Linux) echo "linux" ;;
    *)
      echo "unsupported OS: $(uname -s)" >&2
      exit 1
      ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64 | amd64) echo "amd64" ;;
    arm64 | aarch64) echo "arm64" ;;
    *)
      echo "unsupported architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac
}

verify_checksum() {
  checksums="$1"
  archive_path="$2"
  archive_name="$3"

  expected="$(awk -v archive="$archive_name" '$2 == archive {print $1; exit}' "$checksums")"
  if [ -z "$expected" ]; then
    echo "checksum not found for $archive_name" >&2
    exit 1
  fi

  if command -v sha256sum >/dev/null 2>&1; then
    actual="$(sha256sum "$archive_path" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$archive_path" | awk '{print $1}')"
  else
    echo "missing required command: sha256sum or shasum" >&2
    exit 1
  fi

  if [ "$actual" != "$expected" ]; then
    echo "checksum mismatch for $archive_name" >&2
    echo "expected: $expected" >&2
    echo "actual:   $actual" >&2
    exit 1
  fi
}

path_contains() {
  case ":$PATH:" in
    *":$1:"*) return 0 ;;
    *) return 1 ;;
  esac
}

print_install_dir_help() {
  cat >&2 <<EOF
Cannot write to $INSTALL_DIR.

Try one of these:
  curl -fsSL https://raw.githubusercontent.com/${OWNER}/${REPO}/main/scripts/install.sh | sudo sh
  curl -fsSL https://raw.githubusercontent.com/${OWNER}/${REPO}/main/scripts/install.sh | env INSTALL_DIR="\$HOME/.local/bin" sh

If you use INSTALL_DIR="\$HOME/.local/bin", add this to your shell profile:
  export PATH="\$HOME/.local/bin:\$PATH"
EOF
}

ensure_install_dir() {
  if ! mkdir -p "$INSTALL_DIR" 2>/dev/null; then
    print_install_dir_help
    exit 1
  fi

  if [ ! -w "$INSTALL_DIR" ]; then
    print_install_dir_help
    exit 1
  fi
}

print_path_help() {
  if path_contains "$INSTALL_DIR"; then
    return
  fi

  cat <<EOF

$INSTALL_DIR is not on your PATH. Add it as follows for your current session:
  export PATH="$INSTALL_DIR:\$PATH"

Or add it permanently to your shell profile:
  zsh:  printf '\nexport PATH="$INSTALL_DIR:\$PATH"\n' >> ~/.zshrc
  bash: printf '\nexport PATH="$INSTALL_DIR:\$PATH"\n' >> ~/.bashrc
  fish: printf '\nfish_add_path "$INSTALL_DIR"\n' >> ~/.config/fish/config.fish
EOF
}

resolve_version() {
  if [ "$VERSION" != "latest" ]; then
    echo "$VERSION"
    return
  fi

  curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/${OWNER}/${REPO}/releases/latest" |
    sed 's#.*/##'
}

need curl
need tar
need sed
need awk

os="$(detect_os)"
arch="$(detect_arch)"
tag="$(resolve_version)"
archive="${REPO}_${os}_${arch}.tar.gz"
url="https://github.com/${OWNER}/${REPO}/releases/download/${tag}/${archive}"
checksums_url="https://github.com/${OWNER}/${REPO}/releases/download/${tag}/checksums.txt"
tmpdir="$(mktemp -d)"

cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT INT TERM

curl -fsSL "$url" -o "$tmpdir/$archive"
curl -fsSL "$checksums_url" -o "$tmpdir/checksums.txt"
verify_checksum "$tmpdir/checksums.txt" "$tmpdir/$archive" "$archive"
tar -xzf "$tmpdir/$archive" -C "$tmpdir" "$BINARY"

ensure_install_dir
if ! mv "$tmpdir/$BINARY" "$INSTALL_DIR/$BINARY" 2>/dev/null; then
  print_install_dir_help
  exit 1
fi
chmod 0755 "$INSTALL_DIR/$BINARY"

echo "installed $BINARY $tag to $INSTALL_DIR/$BINARY"
print_path_help

cat <<EOF

To enable shell completions:
  zsh:  $BINARY completion zsh > "\${fpath[1]}/_$BINARY"
  bash: $BINARY completion bash > ~/.local/share/bash-completion/completions/$BINARY
  fish: $BINARY completion fish > ~/.config/fish/completions/$BINARY.fish
EOF

#!/usr/bin/env bash
set -euo pipefail

# Install wash's build toolchain without root:
#   - Go under ~/.local/wash-toolchain/go
#   - Node.js/npm under ~/.local/wash-toolchain/node
#   - pnpm under ~/.local/wash-toolchain/npm-global
# Then add a managed PATH block to ~/.profile and ~/.bashrc.
#
# Overrides:
#   WASH_TOOLCHAIN_DIR=$HOME/.local/wash-toolchain
#   GO_VERSION=1.25.10
#   NODE_VERSION=v22.17.0  OR  NODE_MAJOR=22
#   PNPM_VERSION=11

die() {
  printf 'install-local-toolchain: %s\n' "$*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

repo_root() {
  local dir
  dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
  printf '%s\n' "$dir"
}

default_go_version() {
  local mod
  mod="$(repo_root)/go.mod"
  if [[ -f "$mod" ]]; then
    awk '
      /^toolchain go/ { sub(/^go/, "", $2); print $2; found=1; exit }
      /^go / && !fallback { fallback=$2 }
      END { if (!found && fallback) print fallback }
    ' "$mod"
  fi
}

os_name() {
  case "$(uname -s)" in
    Linux) printf 'linux' ;;
    Darwin) printf 'darwin' ;;
    *) die "unsupported OS: $(uname -s)" ;;
  esac
}

go_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64' ;;
    aarch64|arm64) printf 'arm64' ;;
    armv6l) printf 'armv6l' ;;
    armv7l) printf 'armv6l' ;;
    *) die "unsupported Go arch: $(uname -m)" ;;
  esac
}

node_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'x64' ;;
    aarch64|arm64) printf 'arm64' ;;
    armv7l) printf 'armv7l' ;;
    *) die "unsupported Node arch: $(uname -m)" ;;
  esac
}

latest_node_version() {
  local major="${NODE_MAJOR:-22}"
  curl -fsSL https://nodejs.org/dist/index.json \
    | sed -n "s/.*\"version\":\"\\(v${major}\\.[^\"]*\\)\".*/\\1/p" \
    | awk 'NF && !seen { print; seen=1 }'
}

download() {
  local url="$1" out="$2"
  printf 'Downloading %s\n' "$url"
  curl -fL --retry 3 --retry-delay 2 -o "$out" "$url"
}

install_go() {
  local version="$1" os="$2" arch="$3" root="$4"
  local target="$root/go-$version" archive tmp url
  if [[ -x "$target/bin/go" ]]; then
    printf 'Go %s already installed\n' "$version"
  else
    tmp=$(mktemp -d)
    archive="$tmp/go.tar.gz"
    url="https://go.dev/dl/go${version}.${os}-${arch}.tar.gz"
    download "$url" "$archive"
    tar -C "$tmp" -xzf "$archive"
    rm -rf "$target"
    mv "$tmp/go" "$target"
    rm -rf "$tmp"
  fi
  ln -sfn "$target" "$root/go"
}

install_node() {
  local version="$1" os="$2" arch="$3" root="$4"
  local name="node-${version}-${os}-${arch}"
  local target="$root/$name" archive tmp url
  if [[ -x "$target/bin/node" ]]; then
    printf 'Node %s already installed\n' "$version"
  else
    tmp=$(mktemp -d)
    archive="$tmp/node.tar.xz"
    url="https://nodejs.org/dist/${version}/${name}.tar.xz"
    download "$url" "$archive"
    tar -C "$tmp" -xJf "$archive"
    rm -rf "$target"
    mv "$tmp/$name" "$target"
    rm -rf "$tmp"
  fi
  ln -sfn "$target" "$root/node"
}

install_pnpm() {
  local root="$1" version="$2"
  mkdir -p "$root/npm-global" "$root/pnpm-home"
  "$root/node/bin/npm" install -g --prefix "$root/npm-global" "pnpm@${version}"
}

profile_block() {
  cat <<'EOF'
# >>> wash local toolchain >>>
export WASH_TOOLCHAIN_DIR="${WASH_TOOLCHAIN_DIR:-$HOME/.local/wash-toolchain}"
export GOROOT="$WASH_TOOLCHAIN_DIR/go"
export GOPATH="${GOPATH:-$HOME/go}"
export PNPM_HOME="$WASH_TOOLCHAIN_DIR/pnpm-home"
export PATH="$GOROOT/bin:$GOPATH/bin:$WASH_TOOLCHAIN_DIR/node/bin:$WASH_TOOLCHAIN_DIR/npm-global/bin:$PNPM_HOME:$PATH"
# <<< wash local toolchain <<<
EOF
}

update_profile() {
  local file="$1" tmp
  tmp=$(mktemp)
  if [[ -f "$file" ]]; then
    sed '/^# >>> wash local toolchain >>>$/,/^# <<< wash local toolchain <<<$/{d;}' "$file" > "$tmp"
  fi
  {
    cat "$tmp"
    printf '\n'
    profile_block
  } > "$file"
  rm -f "$tmp"
  printf 'Updated %s\n' "$file"
}

main() {
  need awk
  need curl
  need sed
  need tar

  local root os arch node_os node_arch_name go_version node_version pnpm_version
  root="${WASH_TOOLCHAIN_DIR:-$HOME/.local/wash-toolchain}"
  os="$(os_name)"
  arch="$(go_arch)"
  node_os="$os"
  node_arch_name="$(node_arch)"
  go_version="${GO_VERSION:-$(default_go_version)}"
  [[ -n "$go_version" ]] || die "could not infer Go version; set GO_VERSION=1.25.10"
  node_version="${NODE_VERSION:-$(latest_node_version)}"
  [[ -n "$node_version" ]] || die "could not resolve Node version; set NODE_VERSION=v22.x.y"
  pnpm_version="${PNPM_VERSION:-11}"

  mkdir -p "$root"
  install_go "$go_version" "$os" "$arch" "$root"
  install_node "$node_version" "$node_os" "$node_arch_name" "$root"
  install_pnpm "$root" "$pnpm_version"

  if ! command -v make >/dev/null 2>&1; then
    printf 'Warning: make is still required by wash and was not found in PATH.\n' >&2
  fi

  update_profile "$HOME/.profile"
  update_profile "$HOME/.bashrc"

  printf '\nInstalled:\n'
  "$root/go/bin/go" version
  "$root/node/bin/node" --version
  "$root/node/bin/npm" --version
  "$root/npm-global/bin/pnpm" --version
  printf '\nRestart your shell, or run:\n  source ~/.profile\n'
}

main "$@"

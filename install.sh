#!/bin/sh
set -eu

repo="pedromvgomes/gt"
version="${GT_VERSION:-latest}"

die() {
  echo "ERROR: $*" >&2
  exit 1
}

die_usage() {
  echo "ERROR: $*" >&2
  exit 2
}

need_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

detect_os() {
  case "$(uname -s)" in
    Darwin) echo "darwin" ;;
    Linux) echo "linux" ;;
    *) die_usage "unsupported OS: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    arm64 | aarch64) echo "arm64" ;;
    x86_64) echo "amd64" ;;
    *) die_usage "unsupported architecture: $(uname -m)" ;;
  esac
}

resolve_version() {
  if [ "$version" != "latest" ]; then
    echo "$version"
    return
  fi
  curl -fsSL "https://api.github.com/repos/$repo/releases/latest" |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
    sed -n '1p'
}

download() {
  url="$1"
  out="$2"
  curl -fsSL "$url" -o "$out"
}

sha256_file() {
  file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
    return
  fi
  die "sha256sum or shasum is required to verify checksums"
}

install_dir() {
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    echo /usr/local/bin
    return
  fi
  dir="${HOME:-}/.local/bin"
  [ -n "$dir" ] || die "HOME is required when /usr/local/bin is not writable"
  mkdir -p "$dir"
  echo "$dir"
}

completion_path() {
  shell_name="$1"
  case "$shell_name" in
    zsh)
      if command -v brew >/dev/null 2>&1; then
        brew_prefix="$(brew --prefix 2>/dev/null || true)"
        if [ -n "$brew_prefix" ] && [ -d "$brew_prefix/share/zsh/site-functions" ] && [ -w "$brew_prefix/share/zsh/site-functions" ]; then
          echo "$brew_prefix/share/zsh/site-functions/_gt"
          return
        fi
      fi
      echo "${HOME:-}/.zsh/completions/_gt"
      ;;
    bash) echo "${HOME:-}/.local/share/bash-completion/completions/gt" ;;
    fish) echo "${HOME:-}/.config/fish/completions/gt.fish" ;;
    *) return 1 ;;
  esac
}

completion_note() {
  shell_name="$1"
  path="$2"
  case "$shell_name" in
    zsh)
      case "$path" in
        */site-functions/_gt) echo "restart your shell (e.g. 'exec zsh') to load completions" ;;
        *) echo "add 'fpath=(~/.zsh/completions \$fpath)' to ~/.zshrc before 'compinit', then restart your shell" ;;
      esac
      ;;
    bash) echo "restart your shell or source your bash completion setup" ;;
    fish) echo "restart fish or run 'source ~/.config/fish/config.fish'" ;;
  esac
}

install_completion() {
  gt_bin="$1"
  shell_name="$2"
  path="$(completion_path "$shell_name")" || die_usage "unsupported completion shell: $shell_name"
  [ -n "${HOME:-}" ] || die "HOME is required to install completions"
  mkdir -p "$(dirname "$path")"
  "$gt_bin" completion "$shell_name" >"$path"
  echo "gt $shell_name completions installed at $path"
  note="$(completion_note "$shell_name" "$path")"
  [ -z "$note" ] || echo "Note: $note"
}

prompt_completion() {
  gt_bin="$1"
  shell_name="$(basename "${SHELL:-}")"
  path="$(completion_path "$shell_name" 2>/dev/null || true)"
  [ -n "$path" ] || return 0
  printf "Install %s completions to %s? [Y/n] " "$shell_name" "$path"
  read answer || answer=""
  case "$answer" in
    "" | y | Y | yes | YES) install_completion "$gt_bin" "$shell_name" ;;
    *) echo "Skipping shell completions" ;;
  esac
}

main() {
  need_cmd curl
  need_cmd tar
  need_cmd awk
  need_cmd sed

  os="$(detect_os)"
  arch="$(detect_arch)"
  tag="$(resolve_version)"
  [ -n "$tag" ] || die "could not resolve latest release version"

  asset="gt_${os}_${arch}.tar.gz"
  base_url="https://github.com/$repo/releases/download/$tag"
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' EXIT HUP INT TERM

  download "$base_url/$asset" "$tmp/$asset"
  download "$base_url/checksums.txt" "$tmp/checksums.txt"

  expected="$(awk -v asset="$asset" '$2 == asset {print $1}' "$tmp/checksums.txt")"
  [ -n "$expected" ] || die "checksum for $asset not found"
  actual="$(sha256_file "$tmp/$asset")"
  [ "$actual" = "$expected" ] || die "checksum verification failed for $asset"

  tar -xzf "$tmp/$asset" -C "$tmp"
  [ -f "$tmp/gt" ] || die "archive did not contain gt binary"

  dir="$(install_dir)"
  cp "$tmp/gt" "$dir/gt"
  chmod 755 "$dir/gt"

  case ":$PATH:" in
    *":$dir:"*) ;;
    *) echo "Note: add $dir to your PATH" ;;
  esac

  if [ -t 0 ]; then
    prompt_completion "$dir/gt"
  elif [ -n "${GT_INSTALL_COMPLETIONS:-}" ]; then
    install_completion "$dir/gt" "$GT_INSTALL_COMPLETIONS"
  fi

  echo "gt installed at $dir/gt"
}

main "$@"

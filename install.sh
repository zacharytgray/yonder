#!/bin/sh
# install yonder from a github release.
#   curl -fsSL https://raw.githubusercontent.com/zacharytgray/yonder/main/install.sh | sh
# env: YONDER_VERSION (like v0.1.0, default latest), YONDER_INSTALL_DIR (default ~/.local/bin)
set -eu

# all in main, called on the last line, so a cut-off download runs nothing
main() {
  repo="zacharytgray/yonder"
  dir="${YONDER_INSTALL_DIR:-$HOME/.local/bin}"

  say() { printf '%s\n' "$*"; }
  die() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

  have() { command -v "$1" >/dev/null 2>&1; }

  have curl || die "curl is required"
  have tar || die "tar is required"

  # os and arch, named the way .goreleaser.yaml names its archives
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) die "unsupported os: $(uname -s). yonder supports macos and linux only" ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) die "unsupported cpu: $(uname -m). yonder supports amd64 and arm64 only" ;;
  esac

  # sha256 tool
  if have shasum; then
    sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
  elif have sha256sum; then
    sha256() { sha256sum "$1" | cut -d' ' -f1; }
  else
    die "need shasum or sha256sum to verify the download"
  fi

  tag="${YONDER_VERSION:-}"
  if [ -z "$tag" ]; then
    say "finding the latest release"
    tag=$(curl -fsSL "https://api.github.com/repos/$repo/releases/latest" |
      sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1) || true
    [ -n "$tag" ] || die "could not find the latest release; set YONDER_VERSION=vX.Y.Z"
  fi
  case "$tag" in v*) ;; *) tag="v$tag" ;; esac
  version="${tag#v}"

  archive="yonder_${version}_${os}_${arch}.tar.gz"
  base="https://github.com/$repo/releases/download/$tag"

  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT INT TERM

  say "downloading yonder $tag ($os/$arch)"
  curl -fsSL -o "$tmp/$archive" "$base/$archive" || die "download failed: $base/$archive"
  curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt" || die "download failed: $base/checksums.txt"

  want=$(awk -v f="$archive" '$2 == f { print $1 }' "$tmp/checksums.txt")
  [ -n "$want" ] || die "$archive is not listed in checksums.txt"
  got=$(sha256 "$tmp/$archive")
  [ "$want" = "$got" ] || die "checksum mismatch for $archive (want $want, got $got)"

  tar -xzf "$tmp/$archive" -C "$tmp" yonder || die "could not unpack $archive"
  mkdir -p "$dir" || die "could not create $dir"
  mv "$tmp/yonder" "$dir/yonder.new" && chmod 755 "$dir/yonder.new" && mv "$dir/yonder.new" "$dir/yonder" ||
    die "could not write to $dir; set YONDER_INSTALL_DIR to a folder you own"

  say "installed $dir/yonder"
  "$dir/yonder" version || true

  case ":$PATH:" in
    *":$dir:"*) ;;
    *) say "warning: $dir is not on your PATH. add this to your shell profile:"
       say "  export PATH=\"$dir:\$PATH\"" ;;
  esac

  say "next: yonder init"
}

main "$@"

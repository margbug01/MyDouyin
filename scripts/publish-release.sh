#!/bin/bash
set -euo pipefail

repo=${MYDOUYIN_REPO:-$(cd "$(dirname "$0")/.." && pwd)}
build_root="$repo/build.noindex"
bucket=${R2_BUCKET:-almago-releases}
prefix=${R2_PREFIX:-mydouyin}
public_url=${R2_PUBLIC_URL:-https://dl.imclawd.tech/mydouyin}
gh_repo=${GH_REPO:-margbug01/MyDouyin}

release_dir=""
r2_only=0
github_only=0
dry_run=0
version_override=""

die() { printf 'publish-release: %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Usage: scripts/publish-release.sh [options] [release-dir]

Dual-publishes a MyDouyin release built by scripts/build-release.sh:
  1. Uploads artifacts to Cloudflare R2 (almago-releases/mydouyin) via wrangler
     (immutable installers/archives/deltas first, then no-cache manifests/scripts).
  2. Creates or updates the corresponding GitHub Release v<version> so older
     clients and GitHub visitors stay seamlessly bridged.

Options:
  --version <ver>   Override release version (default: inferred from manifest/mygo.json)
  --r2-only         Upload to Cloudflare R2 only (skip GitHub Releases)
  --github-only     Upload to GitHub Releases only (skip Cloudflare R2)
  --dry-run         Print planned uploads without mutating R2 or GitHub
  -h, --help        Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --r2-only) r2_only=1 ;;
    --github-only) github_only=1 ;;
    --dry-run) dry_run=1 ;;
    --version)
      [[ $# -ge 2 ]] || die "missing value for --version"
      version_override=$2; shift ;;
    --version=*)
      version_override=${1#*=} ;;
    -*)
      die "unknown option: $1" ;;
    *)
      [[ -z $release_dir ]] || die "unexpected extra argument: $1"
      release_dir=$1 ;;
  esac
  shift
done

if [[ $r2_only -eq 1 && $github_only -eq 1 ]]; then
  die "--r2-only and --github-only cannot be used together"
fi

if [[ -z $release_dir ]]; then
  release_dir=$(ls -dt "$build_root"/release-* 2>/dev/null | head -n 1 || true)
fi
[[ -n $release_dir && -d $release_dir ]] || die "release directory not found (pass a path or run ./scripts/build-release.sh first)"

# Collect publishable files from the release output directory (both root and platform subdirs).
immutable_files=()
mutable_files=()
all_files=()

collect_file() {
  local f=$1
  local base
  base=$(basename "$f")
  case "$base" in
    SOURCE_COMMIT|.DS_Store|*.app|*.exe.sig) return 0 ;;
    update-*.json|SHA256SUMS|install.sh|install.ps1)
      mutable_files+=("$f")
      all_files+=("$f")
      ;;
    *.dmg|*.exe|*.tar.gz|*.delta|*.zip)
      immutable_files+=("$f")
      all_files+=("$f")
      ;;
  esac
}

while IFS= read -r -d '' f; do
  collect_file "$f"
done < <(find "$release_dir" -maxdepth 2 -type f -print0 | sort -z)

[[ ${#all_files[@]} -gt 0 ]] || die "no release artifacts found in $release_dir"

detect_version() {
  if [[ -n $version_override ]]; then
    printf '%s\n' "${version_override#v}"
    return
  fi
  local m
  for m in "${mutable_files[@]}"; do
    if [[ $(basename "$m") == update-*.json ]]; then
      python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$m"
      return
    fi
  done
  python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["version"])' "$repo/mygo.json"
}

version=$(detect_version)
[[ -n $version ]] || die "could not determine release version"
tag="v${version}"

content_type_for() {
  case "$(basename "$1")" in
    *.json) echo "application/json; charset=utf-8" ;;
    *.sh|*.ps1|SHA256SUMS) echo "text/plain; charset=utf-8" ;;
    *.tar.gz) echo "application/gzip" ;;
    *.dmg) echo "application/x-apple-diskimage" ;;
    *.exe) echo "application/vnd.microsoft.portable-executable" ;;
    *) echo "application/octet-stream" ;;
  esac
}

upload_r2_file() {
  local file=$1
  local cache_control=$2
  local base key ct
  base=$(basename "$file")
  key="${prefix}/${base}"
  ct=$(content_type_for "$file")
  if [[ $dry_run -eq 1 ]]; then
    printf '[dry-run] R2 put %s/%s (%s, %s)\n' "$bucket" "$key" "$ct" "$cache_control"
    return 0
  fi
  printf 'R2 uploading %s -> %s/%s\n' "$base" "$bucket" "$key"
  wrangler r2 object put "${bucket}/${key}" \
    --remote \
    --file "$file" \
    --content-type "$ct" \
    --cache-control "$cache_control" >/dev/null
}

printf 'Publishing MyDouyin %s from %s\n' "$tag" "$release_dir"
printf '  Immutable files: %d\n' "${#immutable_files[@]}"
printf '  Mutable files:   %d\n' "${#mutable_files[@]}"

if [[ $github_only -eq 0 ]]; then
  command -v wrangler >/dev/null 2>&1 || die "wrangler not found (install via brew install cloudflare-wrangler)"
  # Upload archives, installers, and deltas first so manifests never reference missing objects.
  for f in ${immutable_files[@]+"${immutable_files[@]}"}; do
    upload_r2_file "$f" "public, max-age=31536000, immutable"
  done
  for f in ${mutable_files[@]+"${mutable_files[@]}"}; do
    upload_r2_file "$f" "no-cache"
  done
  printf 'Cloudflare R2 updated: %s/\n' "$public_url"
fi

if [[ $r2_only -eq 0 ]]; then
  command -v gh >/dev/null 2>&1 || die "gh CLI not found"
  if [[ $dry_run -eq 1 ]]; then
    printf '[dry-run] GitHub Release %s (%s): %d files\n' "$tag" "$gh_repo" "${#all_files[@]}"
  else
    if gh release view "$tag" -R "$gh_repo" >/dev/null 2>&1; then
      printf 'Updating existing GitHub Release %s...\n' "$tag"
      gh release upload "$tag" -R "$gh_repo" --clobber "${all_files[@]}"
    else
      notes_file=$(mktemp "${TMPDIR:-/tmp}/mydouyin-notes.XXXXXX")
      trap 'rm -f "$notes_file"' EXIT
      python3 - "$repo/CHANGELOG.md" "$version" "$public_url" > "$notes_file" <<'PY'
import re, sys, pathlib
changelog = pathlib.Path(sys.argv[1]).read_text() if pathlib.Path(sys.argv[1]).exists() else ""
ver, url = sys.argv[2], sys.argv[3]
m = re.search(rf'^##\s+\[?v?{re.escape(ver)}\]?.*?\n(.*?)(?=^##\s+|\Z)', changelog, re.M | re.S)
body = m.group(1).strip() if m else f"MyDouyin v{ver}"
print(body)
print(f"\n---\nCloudflare R2 加速源：`{url}/`")
PY
      printf 'Creating GitHub Release %s...\n' "$tag"
      gh release create "$tag" -R "$gh_repo" --title "MyDouyin ${tag}" --notes-file "$notes_file" "${all_files[@]}"
    fi
  fi
fi

printf 'Done publishing %s\n' "$tag"

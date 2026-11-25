#!/bin/bash
set -euo pipefail

repo=${MYDOUYIN_REPO:-$(cd "$(dirname "$0")/.." && pwd)}
build_root="$repo/build.noindex"
keep_releases=${KEEP_RELEASES:-1}
key_file=${MYGO_UPDATER_KEY_FILE:-$HOME/Library/Application Support/mygo/update-keys/MyDouyin/mygo-update.key}
platforms=${PLATFORMS:-darwin/arm64,windows/amd64}
prune_only=0
mygo_args=()

die() { printf 'build-release: %s\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Usage: scripts/build-release.sh [options] [mygo build flags...]

Builds MyDouyin from a committed Git snapshot in a temporary directory (always
deleted on exit) and prunes old build.noindex/release-* directories so disk
usage stays bounded. Windows uses system Media Foundation via mygo/ui/video
with no bundled FFmpeg binaries.

Options:
  --platform, -platform <list>  Target platforms (default: darwin/arm64,windows/amd64)
  --keep <N>                    Number of release-* dirs to keep (default: 1;
                                0 allowed only with --prune-only)
  --prune-only                  Only prune build.noindex/release-* without building
  -h, --help                    Show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)
      usage; exit 0 ;;
    --prune-only)
      prune_only=1 ;;
    --keep)
      [[ $# -ge 2 ]] || die "missing value for $1"
      keep_releases=$2; shift ;;
    --keep=*)
      keep_releases=${1#*=} ;;
    -platform|--platform)
      [[ $# -ge 2 ]] || die "missing value for $1"
      platforms=$2; shift ;;
    -platform=*|--platform=*)
      platforms=${1#*=} ;;
    -o|-o=*|--o|--o=*|--)
      die "output directory is managed by build-release.sh" ;;
    -sign|--sign)
      [[ $# -ge 2 ]] || die "missing value for $1"
      mygo_args+=("$1" "$2"); shift ;;
    -*)
      mygo_args+=("$1") ;;
    *)
      die "unexpected argument: $1 (pass flags only; source comes from BUILD_REF/HEAD)" ;;
  esac
  shift
done

[[ $keep_releases =~ ^[0-9]+$ ]] || die "--keep must be a non-negative integer: $keep_releases"
if [[ $prune_only -eq 0 && $keep_releases -lt 1 ]]; then
  die "--keep must be >= 1 when building (use --prune-only --keep 0 to clear all releases)"
fi

prune_releases() {
  local keep=$1
  local protect=${2:-}
  [[ -d $build_root ]] || return 0

  local dirs=()
  local d
  while IFS= read -r d; do
    [[ -n $d && -d $d ]] || continue
    [[ -n $protect && $d == "$protect" ]] && continue
    dirs+=("$d")
  done < <(ls -dt "$build_root"/release-* 2>/dev/null || true)

  local remain=$keep
  if [[ -n $protect ]]; then
    remain=$((keep > 0 ? keep - 1 : 0))
  fi

  local idx=0
  for d in ${dirs[@]+"${dirs[@]}"}; do
    # Remove any leftover src/ snapshot inside kept releases from old manual builds.
    if [[ $idx -lt $remain ]]; then
      [[ -d "$d/src" ]] && rm -rf "$d/src"
    else
      rm -rf "$d"
    fi
    idx=$((idx + 1))
  done
}

if [[ $prune_only -eq 1 ]]; then
  prune_releases "$keep_releases"
  exit 0
fi

commit=$(git -C "$repo" rev-parse --verify --end-of-options "${BUILD_REF:-HEAD}^{commit}")
short_commit=${commit:0:12}

# Load updater private key automatically when available locally.
if [[ -z ${MYGO_UPDATER_PRIVATE_KEY:-} && -r $key_file ]]; then
  MYGO_UPDATER_PRIVATE_KEY=$(cat "$key_file")
  export MYGO_UPDATER_PRIVATE_KEY
fi

snapshot=$(mktemp -d "${TMPDIR:-/tmp}/mydouyin-release-source.XXXXXX")
mkdir -p "$build_root"
output=""
cleanup() {
  rm -rf "$snapshot"
  if [[ -n $output && ! -f "$output/SOURCE_COMMIT" ]]; then
    rm -rf "$output"
  fi
}
trap cleanup EXIT

git -C "$repo" archive "$commit" | tar -xf - -C "$snapshot"
version=$(plutil -extract version raw -o - "$snapshot/mygo.json" 2>/dev/null || echo "dev")
output=$(mktemp -d "$build_root/release-${version}-${short_commit}.XXXXXX")

printf 'Building MyDouyin %s (%s) for %s\nOutput: %s\n' "$version" "$short_commit" "$platforms" "$output"
# makensis aborts under the C locale when launched from GUI apps.
export LC_ALL=en_US.UTF-8
(
  cd "$snapshot"
  go tool mygo build -platform "$platforms" ${mygo_args[@]+"${mygo_args[@]}"} -o "$output"
)

printf '%s\n' "$commit" > "$output/SOURCE_COMMIT"
prune_releases "$keep_releases" "$output"
printf 'Built %s (%s)\nOutput: %s\n' "$version" "$short_commit" "$output"

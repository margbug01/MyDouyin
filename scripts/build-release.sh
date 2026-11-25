#!/bin/bash
set -euo pipefail

repo=${MYDOUYIN_REPO:-$(cd "$(dirname "$0")/.." && pwd)}
build_root="$repo/build.noindex"
keep_releases=${KEEP_RELEASES:-1}
win_ffmpeg_dir=${MYDOUYIN_WIN_FFMPEG_DIR:-$HOME/Library/Caches/mydouyin-win-ffmpeg}
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
usage stays bounded.

Options:
  --platform, -platform <list>  Target platforms (default: darwin/arm64,windows/amd64)
  --win-ffmpeg <dir>            Shared cache dir for ffmpeg.exe/ffprobe.exe
                                (default: ~/Library/Caches/mydouyin-win-ffmpeg)
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
    --win-ffmpeg)
      [[ $# -ge 2 ]] || die "missing value for $1"
      win_ffmpeg_dir=$2; shift ;;
    --win-ffmpeg=*)
      win_ffmpeg_dir=${1#*=} ;;
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

copy_fast() {
  cp -c "$1" "$2" 2>/dev/null || cp "$1" "$2"
}

ensure_win_ffmpeg() {
  if [[ -f "$win_ffmpeg_dir/ffmpeg.exe" && -f "$win_ffmpeg_dir/ffprobe.exe" ]]; then
    return 0
  fi
  if [[ ${NO_FETCH_FFMPEG:-0} == 1 ]]; then
    die "missing ffmpeg.exe/ffprobe.exe in $win_ffmpeg_dir"
  fi
  command -v gh >/dev/null 2>&1 || die "missing ffmpeg.exe/ffprobe.exe in $win_ffmpeg_dir (and gh CLI unavailable to seed cache)"

  local gh_repo
  gh_repo=$(plutil -extract updates.github raw -o - "$repo/mygo.json" 2>/dev/null || true)
  [[ -n $gh_repo ]] || die "missing updates.github in mygo.json; place ffmpeg.exe/ffprobe.exe in $win_ffmpeg_dir"

  local fetch_dir
  fetch_dir=$(mktemp -d "${TMPDIR:-/tmp}/mydouyin-ffmpeg-fetch.XXXXXX")
  trap 'rm -rf "$fetch_dir"' RETURN
  printf 'Seeding Windows FFmpeg cache in %s from latest %s release...\n' "$win_ffmpeg_dir" "$gh_repo"
  gh release download --repo "$gh_repo" --pattern 'mydouyin-*-windows-amd64.tar.gz' --dir "$fetch_dir" >/dev/null
  local tar_file
  tar_file=$(ls "$fetch_dir"/mydouyin-*-windows-amd64.tar.gz 2>/dev/null | head -n 1 || true)
  [[ -f $tar_file ]] || die "no windows-amd64.tar.gz found in latest $gh_repo release"

  mkdir -p "$win_ffmpeg_dir"
  tar -xzf "$tar_file" -C "$fetch_dir"
  local f found
  for f in ffmpeg.exe ffprobe.exe FFmpeg-LICENSE.txt FFmpeg-README.txt; do
    found=$(find "$fetch_dir" -maxdepth 3 -type f -name "$f" | head -n 1 || true)
    if [[ -n $found ]]; then
      mv -f "$found" "$win_ffmpeg_dir/$f"
    fi
  done
  [[ -f "$win_ffmpeg_dir/ffmpeg.exe" && -f "$win_ffmpeg_dir/ffprobe.exe" ]] || \
    die "downloaded archive did not contain ffmpeg.exe and ffprobe.exe"
}

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

if [[ ",$platforms," == *",windows/"* ]]; then
  ensure_win_ffmpeg
  win_res="$snapshot/resources/windows-amd64"
  mkdir -p "$win_res"
  for f in ffmpeg.exe ffprobe.exe; do
    copy_fast "$win_ffmpeg_dir/$f" "$win_res/$f"
  done
  for f in FFmpeg-LICENSE.txt FFmpeg-README.txt; do
    [[ -f "$win_ffmpeg_dir/$f" ]] && copy_fast "$win_ffmpeg_dir/$f" "$win_res/$f"
  done
  {
    printf 'MyDouyin source commit: %s\n' "$commit"
    printf 'Built: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'ffmpeg.exe SHA256: %s\n' "$(shasum -a 256 "$win_res/ffmpeg.exe" | awk '{print $1}')"
    printf 'ffprobe.exe SHA256: %s\n' "$(shasum -a 256 "$win_res/ffprobe.exe" | awk '{print $1}')"
  } > "$win_res/BUILD-INFO.txt"
fi

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

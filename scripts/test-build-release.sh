#!/bin/bash
set -euo pipefail

script_dir=$(cd "$(dirname "$0")" && pwd)
build_script="$script_dir/build-release.sh"

tmp=$(mktemp -d "${TMPDIR:-/tmp}/mydouyin-test-build-release.XXXXXX")
trap 'rm -rf "$tmp"' EXIT

repo="$tmp/repo"
mkdir -p "$repo"
git -C "$repo" init -q
git -C "$repo" config user.name test
git -C "$repo" config user.email test@example.com
cat >"$repo/mygo.json" <<'EOF'
{
  "name": "MyDouyin",
  "identifier": "dev.margbug.mydouyin",
  "version": "0.2.99",
  "out": "build.noindex",
  "updates": {
    "github": "margbug01/MyDouyin"
  }
}
EOF
git -C "$repo" add mygo.json
git -C "$repo" commit -qm "init"

fake_bin="$tmp/bin"
mkdir -p "$fake_bin"
cat >"$fake_bin/go" <<'EOF'
#!/bin/bash
set -euo pipefail
out=""
prev=""
for arg in "$@"; do
  if [[ $prev == "-o" ]]; then
    out=$arg
    break
  fi
  prev=$arg
done
if [[ ${MOCK_GO_FAIL:-0} == 1 ]]; then
  mkdir -p "$out/windows-amd64"
  echo partial >"$out/windows-amd64/partial.bin"
  exit 1
fi
# Verify Windows FFmpeg was staged into the temp snapshot before build.
[[ -f resources/windows-amd64/ffmpeg.exe ]] || { echo "missing ffmpeg.exe in snapshot" >&2; exit 2; }
[[ -f resources/windows-amd64/ffprobe.exe ]] || { echo "missing ffprobe.exe in snapshot" >&2; exit 2; }
[[ -f resources/windows-amd64/BUILD-INFO.txt ]] || { echo "missing BUILD-INFO.txt in snapshot" >&2; exit 2; }
mkdir -p "$out/windows-amd64" "$out/darwin-arm64"
printf 'ok\n' >"$out/windows-amd64/MyDouyin.exe"
printf 'ok\n' >"$out/darwin-arm64/MyDouyin.app"
EOF
chmod +x "$fake_bin/go"

ffmpeg_cache="$tmp/win-ffmpeg"
mkdir -p "$ffmpeg_cache"
printf 'fake-ffmpeg' >"$ffmpeg_cache/ffmpeg.exe"
printf 'fake-ffprobe' >"$ffmpeg_cache/ffprobe.exe"

# Seed 3 old releases (one with a leftover src/ dir) plus an unrelated darwin-arm64 dir.
mkdir -p "$repo/build.noindex/darwin-arm64" \
         "$repo/build.noindex/release-0.2.20/src" \
         "$repo/build.noindex/release-0.2.21/src" \
         "$repo/build.noindex/release-0.2.22/src"
touch -t 202611240100 "$repo/build.noindex/release-0.2.20"
touch -t 202611240200 "$repo/build.noindex/release-0.2.21"
touch -t 202611240300 "$repo/build.noindex/release-0.2.22"

# 1. --prune-only --keep 2 keeps 0.2.21 and 0.2.22, removes 0.2.20, strips leftover src/, leaves darwin-arm64.
MYDOUYIN_REPO="$repo" "$build_script" --prune-only --keep 2
[[ ! -d "$repo/build.noindex/release-0.2.20" ]] || { echo "0.2.20 should be pruned" >&2; exit 1; }
[[ -d "$repo/build.noindex/release-0.2.21" && ! -d "$repo/build.noindex/release-0.2.21/src" ]] || { echo "0.2.21 kept without src" >&2; exit 1; }
[[ -d "$repo/build.noindex/release-0.2.22" && ! -d "$repo/build.noindex/release-0.2.22/src" ]] || { echo "0.2.22 kept without src" >&2; exit 1; }
[[ -d "$repo/build.noindex/darwin-arm64" ]] || { echo "darwin-arm64 must stay" >&2; exit 1; }

# 2. Failed build cleans up its partial output dir and leaves existing releases intact.
if PATH="$fake_bin:$PATH" MYDOUYIN_REPO="$repo" MYDOUYIN_WIN_FFMPEG_DIR="$ffmpeg_cache" NO_FETCH_FFMPEG=1 MOCK_GO_FAIL=1 \
    "$build_script" >/dev/null 2>&1; then
  echo "expected failed build to exit non-zero" >&2
  exit 1
fi
count_after_fail=$(ls -d "$repo"/build.noindex/release-* | wc -l | tr -d ' ')
[[ $count_after_fail == "2" ]] || { echo "expected 2 releases after failed build, got $count_after_fail" >&2; exit 1; }

# 3. Successful build creates new release, prunes down to default KEEP_RELEASES=1, leaves no src/.
PATH="$fake_bin:$PATH" MYDOUYIN_REPO="$repo" MYDOUYIN_WIN_FFMPEG_DIR="$ffmpeg_cache" NO_FETCH_FFMPEG=1 \
  "$build_script" >/dev/null
releases=("$repo"/build.noindex/release-*)
[[ ${#releases[@]} == 1 ]] || { echo "expected 1 release after build, got ${#releases[@]}" >&2; exit 1; }
[[ -f "${releases[0]}/SOURCE_COMMIT" ]] || { echo "missing SOURCE_COMMIT" >&2; exit 1; }
[[ ! -d "${releases[0]}/src" ]] || { echo "release must not contain src/" >&2; exit 1; }
[[ -d "$repo/build.noindex/darwin-arm64" ]] || { echo "darwin-arm64 must stay" >&2; exit 1; }

# 4. --prune-only --keep 0 clears all release-* dirs.
MYDOUYIN_REPO="$repo" "$build_script" --prune-only --keep 0
shopt -s nullglob
remaining=("$repo"/build.noindex/release-*)
[[ ${#remaining[@]} == 0 ]] || { echo "expected 0 releases after --keep 0" >&2; exit 1; }

# 5. --prune-only succeeds cleanly on an empty build.noindex (Bash 3.2 set -u safe).
MYDOUYIN_REPO="$repo" "$build_script" --prune-only

echo "PASS: test-build-release.sh"

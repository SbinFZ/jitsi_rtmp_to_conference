#!/usr/bin/env bash
# Native CLI → actual Jitsi → native CLI → independently decoded artifacts.
set -euo pipefail
cd "$(dirname "$0")/.."
make build
HOST="${JITSI_HOST:-m.yuanjinapp.com}"
ROOM="${JITSI_ROOM:-converter-$(date +%s)-$RANDOM}"
OUT="${VERIFY_OUT:-.artifacts/verify-$ROOM}"
mkdir -p "$OUT/media"
receiver_pid=''; sender_pid=''
cleanup() {
  if [[ -n "$sender_pid" ]]; then kill "$sender_pid" 2>/dev/null || true; fi
  if [[ -n "$receiver_pid" ]]; then kill "$receiver_pid" 2>/dev/null || true; fi
}
trap cleanup EXIT INT TERM
bin/jitsi-receive --host "$HOST" --room "$ROOM" --duration 55s --out "$OUT/media" >"$OUT/receive.log" 2>&1 &
receiver_pid=$!
args=(--host "$HOST" --room "$ROOM" --duration 45s --retries 0)
# STREAM_INPUT can carry a signed URL without exposing it as an argv parameter.
if [[ -z "${STREAM_INPUT:-}" ]]; then args+=(--input test); fi
if [[ -n "${INPUT_PROXY:-}" ]]; then args+=(--input-proxy "$INPUT_PROXY"); fi
bin/jitsi-stream "${args[@]}" >"$OUT/send.log" 2>&1 &
sender_pid=$!
wait "$sender_pid"; sender_pid=''
wait "$receiver_pid"; receiver_pid=''
shopt -s nullglob
videos=("$OUT"/media/*.ivf); audios=("$OUT"/media/*.ogg)
[[ ${#videos[@]} -gt 0 && ${#audios[@]} -gt 0 ]] || { echo 'No media received' >&2; exit 1; }
for f in "${videos[@]}" "${audios[@]}"; do
  ffprobe -v error -count_frames -show_entries stream=codec_name,width,height,sample_rate,channels,nb_read_frames -of compact "$f"
  ffmpeg -v error -i "$f" -f null -
done
echo "Verified: $HOST/$ROOM; artifacts: $OUT"

#!/bin/sh
# SOCPUPPET_UI supervisor start script (Linux / macOS / Docker ENTRYPOINT)
#
# Usage:
#   ./start.sh                       run in the foreground (Ctrl-C to stop)
#   nohup ./start.sh >socpuppet_ui.log 2>&1 &   run persistently in the background
#   ./start.sh -addr :9000           extra arguments are passed through to socpuppet_ui as-is
#
# It does exactly one thing: run socpuppet_ui and, once the process exits, decide by exit code whether to relaunch it.
#
#   0      user stopped normally  -> exit the loop
#   other  crash               -> rerun after backoff (1→2→4... up to 60 seconds)
set -u

cd "$(dirname "$0")" || exit 1

BIN=./socpuppet_ui
[ -x "$BIN" ] || { echo "[socpuppet_ui] executable $BIN not found" >&2; exit 1; }

MAX_DELAY=60

child=0
stopping=0

# Forward stop signals to socpuppet_ui itself.
#
# Under Docker this is required: docker stop sends SIGTERM only to PID 1 (this script),
# never to the child. Without forwarding, socpuppet_ui never sees the signal and cannot shut down gracefully; after 10 seconds it gets
# hard-killed by SIGKILL and any running task is cut off mid-flight.
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# A signal interrupts wait and makes it return >128. At that point the child is still shutting down gracefully;
	# wait must be called once more to get its real exit code.
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[socpuppet_ui] stopped"
		exit 0
	fi

	case "$code" in
		0)
			echo "[socpuppet_ui] exited normally"
			exit 0
			;;
		*)
			echo "[socpuppet_ui] abnormal exit (code=$code), restarting in ${delay}s" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done

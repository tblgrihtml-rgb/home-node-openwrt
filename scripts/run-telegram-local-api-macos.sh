#!/bin/sh
# Temporary local Bot API for large music imports. The files directory must be
# an SMB mount of the router SSD; never fall back to the Mac system disk.
set -eu

if [ "$#" -ne 4 ]; then
	printf '%s\n' 'Usage: run-telegram-local-api-macos.sh PRIVATE_ENV BINARY WORK_DIR SSD_FILES_DIR' >&2
	exit 2
fi

private_env="$1"
binary="$2"
work_dir="$3"
files_dir="$4"

[ -r "$private_env" ] || { printf '%s\n' 'Private environment file not readable' >&2; exit 1; }
[ -x "$binary" ] || { printf '%s\n' 'Telegram Bot API binary not executable' >&2; exit 1; }
[ -d "$files_dir" ] && [ -w "$files_dir" ] || { printf '%s\n' 'SSD files directory unavailable' >&2; exit 1; }
case "$(df -P "$files_dir" | awk 'NR == 2 { print $1 }')" in
	//* ) ;;
	* ) printf '%s\n' 'Files directory is not on the router SMB share' >&2; exit 1 ;;
esac

. "$private_env"
: "${TELEGRAM_API_ID:?Set TELEGRAM_API_ID in the private environment file}"
: "${TELEGRAM_API_HASH:?Set TELEGRAM_API_HASH in the private environment file}"
export TELEGRAM_API_ID TELEGRAM_API_HASH
mkdir -p "$work_dir"
chmod 700 "$work_dir"

caffeinate -i "$binary" --local --http-ip-address=127.0.0.1 --http-port=8081 \
	--dir="$work_dir" --files-dir="$files_dir" --verbosity=0 &
api_pid=$!
cleanup() { kill "$api_pid" 2>/dev/null || true; wait "$api_pid" 2>/dev/null || true; }
trap cleanup INT TERM

while kill -0 "$api_pid" 2>/dev/null; do
	sleep 5
	case "$(df -P "$files_dir" 2>/dev/null | awk 'NR == 2 { print $1 }')" in
		//* ) ;;
		* ) printf '%s\n' 'Router SMB share disconnected; stopping Telegram API' >&2; cleanup; exit 1 ;;
	esac
done
wait "$api_pid"

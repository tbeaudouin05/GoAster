#!/bin/sh
set -eu

app_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
payload="$app_root/Resources/goaster"

if [ ! -x "$payload" ]; then
	printf '%s\n' "GoAster payload is missing or not executable: $payload" >&2
	exit 1
fi

# Finder launches the bundle without a terminal. Ask Terminal to run the
# private payload so the normal interactive login and chat flow remain intact.
/usr/bin/osascript - "$payload" <<'APPLESCRIPT'
on run argv
    if (count of argv) is less than 1 then error "GoAster payload path is missing"
    set payloadPath to item 1 of argv
    tell application "Terminal"
        activate
        do script quoted form of payloadPath
    end tell
end run
APPLESCRIPT

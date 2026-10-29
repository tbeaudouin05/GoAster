#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
dist_dir=${1:-${GOASTER_OUTPUT_DIR:-"$repo_root/dist"}}
case "$dist_dir" in
	/*) ;;
	*) dist_dir="$PWD/$dist_dir" ;;
esac

failures=0
packages=0

fail() {
	printf 'FAIL: %s\n' "$*" >&2
	failures=$((failures + 1))
}

require_file() {
	if [ ! -f "$1" ]; then
		fail "missing file: $1"
	fi
}

require_executable() {
	if [ ! -x "$1" ]; then
		fail "not executable: $1"
	fi
}

require_text() {
	file=$1
	text=$2
	if ! grep -F "$text" "$file" >/dev/null 2>&1; then
		fail "missing '$text' in $file"
	fi
}

validate_linux_launcher() {
	desktop=$1
	test_dir=$(mktemp -d "${TMPDIR:-/tmp}/goaster-desktop-test.XXXXXX")
	if ! mkdir -p "$test_dir/payload"; then
		fail "could not create Linux launcher test directory"
		return
	fi
	printf '%s\n' '#!/bin/sh' "printf '%s\\n' linux-launch-ok" >"$test_dir/payload/goaster"
	chmod 755 "$test_dir/payload/goaster"
	cp "$desktop" "$test_dir/GoAster.desktop"
	if [ "$(sh -c 'exec "$(dirname "$1")/payload/goaster"' GoAster "$test_dir/GoAster.desktop")" != "linux-launch-ok" ]; then
		fail "Linux desktop Exec command did not reach the package payload: $desktop"
	fi
	rm -rf "$test_dir"
}

if [ ! -d "$dist_dir" ]; then
	fail "package directory does not exist: $dist_dir"
fi

if [ -d "$dist_dir" ]; then
	if ! sh -n "$script_dir/build.sh"; then
		fail "packaging/build.sh has shell syntax errors"
	fi
	if ! sh -n "$script_dir/macos/launcher.sh"; then
		fail "macOS launcher has shell syntax errors"
	fi

	for package_dir in "$dist_dir"/GoAster-*; do
		[ -d "$package_dir" ] || continue
		packages=$((packages + 1))
		package_name=$(basename "$package_dir")
		case "$package_name" in
			GoAster-macos-*)
				app="$package_dir/GoAster.app"
				launcher="$app/Contents/MacOS/GoAster"
				payload="$app/Contents/Resources/goaster"
				require_file "$app/Contents/Info.plist"
				require_executable "$launcher"
				require_executable "$payload"
				if [ -e "$package_dir/goaster" ] || [ -e "$package_dir/goaster.exe" ]; then
					fail "macOS raw payload leaked to package root: $package_dir"
				fi
				require_text "$app/Contents/Info.plist" '<key>CFBundleExecutable</key>'
				require_text "$app/Contents/Info.plist" '<string>GoAster</string>'
				require_text "$launcher" '/usr/bin/osascript'
				require_text "$launcher" 'application "Terminal"'
				require_text "$launcher" 'do script'
				require_text "$launcher" 'Resources/goaster'
				require_file "$dist_dir/$package_name.zip"
				;;
			GoAster-windows-*)
				launcher="$package_dir/GoAster.cmd"
				payload="$package_dir/payload/goaster.exe"
				require_file "$launcher"
				require_file "$payload"
				if [ -e "$package_dir/goaster.exe" ] || [ -e "$package_dir/goaster" ]; then
					fail "Windows raw payload leaked to package root: $package_dir"
				fi
				require_text "$launcher" '%~dp0'
				require_text "$launcher" 'payload\goaster.exe'
				require_text "$launcher" '"%PAYLOAD%" %*'
				require_text "$launcher" 'Running the payload directly'
				require_file "$dist_dir/$package_name.zip"
				;;
			GoAster-linux-*)
				desktop="$package_dir/GoAster.desktop"
				payload="$package_dir/payload/goaster"
				require_file "$desktop"
				require_executable "$desktop"
				require_executable "$payload"
				if [ -e "$package_dir/goaster" ] || [ -e "$package_dir/goaster.exe" ]; then
					fail "Linux raw payload leaked to package root: $package_dir"
				fi
				require_text "$desktop" 'Terminal=true'
				require_text "$desktop" 'Exec=sh -c'
				require_text "$desktop" '%k'
				require_text "$desktop" 'payload/goaster'
				validate_linux_launcher "$desktop"
				require_file "$dist_dir/$package_name.tar.gz"
				;;
			*) fail "unrecognized package directory: $package_dir" ;;
		esac
	done
fi

if [ "$packages" -eq 0 ]; then
	fail "no package directories found in $dist_dir"
fi

if [ "$failures" -ne 0 ]; then
	printf '%s package validation error(s)\n' "$failures" >&2
	exit 1
fi

printf 'Validated %s package(s) in %s\n' "$packages" "$dist_dir"

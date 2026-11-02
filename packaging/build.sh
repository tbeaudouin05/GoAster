#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
. "$script_dir/assets.sh"
output_dir=${GOASTER_OUTPUT_DIR:-"$repo_root/dist"}
target=${1:-all}

usage() {
	cat <<'USAGE'
Usage: packaging/build.sh [all|macos|windows|linux] [output-directory]

Builds self-contained GoAster packages. The default builds both macOS
architectures plus amd64 Windows and Linux packages. Set GOASTER_ARCH to
amd64 or arm64 to select one architecture for a platform target.
USAGE
}

die() {
	printf 'packaging/build.sh: %s\n' "$*" >&2
	exit 1
}

if [ "$target" = "-h" ] || [ "$target" = "--help" ]; then
	usage
	exit 0
fi

if [ "$#" -gt 2 ]; then
	usage >&2
	exit 2
fi
if [ "$#" -eq 2 ]; then
	output_dir=$2
fi
case "$output_dir" in
	/*) ;;
	*) output_dir="$PWD/$output_dir" ;;
esac
if [ -z "$output_dir" ] || [ "$output_dir" = "/" ]; then
	die "refusing an empty or root output directory"
fi
mkdir -p "$output_dir"

check_tracked_regular packaging/assets.sh || die "asset checks must be tracked"
check_starter_tree "$repo_root/skills" || die "invalid starter assets"
check_tracked_starters || die "starter assets must be tracked regular files"

stage_dir=
cleanup_stage() {
	if [ -n "${stage_dir:-}" ] && [ -e "$stage_dir" ]; then
		rm -rf "$stage_dir"
	fi
}
trap cleanup_stage 0 1 2 3 15

archive_package() {
	archive_format=$1
	archive_extension=$2
	package_dir=$3
	archive_path="$output_dir/$(basename "$package_dir").$archive_extension"
	temporary_archive=$(mktemp "$output_dir/.$(basename "$package_dir").archive.XXXXXX")
	if ! (cd "$repo_root" && go run ./packaging/cmd/archive \
		--format "$archive_format" \
		--source "$package_dir" \
		--output "$temporary_archive"); then
		rm -f "$temporary_archive"
		die "could not create archive $archive_path"
	fi
	mv -f "$temporary_archive" "$archive_path"
}

build_package() {
	platform=$1
	architecture=$2
	package_name="GoAster-$platform-$architecture"
	package_dir="$output_dir/$package_name"
	stage_dir=$(mktemp -d "$output_dir/.$package_name.stage.XXXXXX")
	package_root="$stage_dir/$package_name"

	case "$platform" in
		macos)
			[ -d "$script_dir/macos" ] && [ ! -L "$script_dir/macos" ] || die "invalid macOS assets directory"
			check_tracked_regular packaging/macos/Info.plist || die "invalid macOS package asset"
			check_tracked_regular packaging/macos/launcher.sh || die "invalid macOS package asset"
			goos=darwin
			binary_path="$package_root/GoAster.app/Contents/Resources/goaster"
			mkdir -p "$package_root/GoAster.app/Contents/MacOS" "$(dirname "$binary_path")"
			cp "$script_dir/macos/Info.plist" "$package_root/GoAster.app/Contents/Info.plist"
			cp "$script_dir/macos/launcher.sh" "$package_root/GoAster.app/Contents/MacOS/GoAster"
			chmod 755 "$package_root/GoAster.app/Contents/MacOS/GoAster"
			archive_format=zip
			archive_extension=zip
			binary_suffix=
			;;
		windows)
			[ -d "$script_dir/windows" ] && [ ! -L "$script_dir/windows" ] || die "invalid Windows assets directory"
			check_tracked_regular packaging/windows/GoAster.cmd || die "invalid Windows package asset"
			goos=windows
			binary_path="$package_root/payload/goaster"
			mkdir -p "$(dirname "$binary_path")"
			cp "$script_dir/windows/GoAster.cmd" "$package_root/GoAster.cmd"
			archive_format=zip
			archive_extension=zip
			binary_suffix=.exe
			;;
		linux)
			[ -d "$script_dir/linux" ] && [ ! -L "$script_dir/linux" ] || die "invalid Linux assets directory"
			check_tracked_regular packaging/linux/GoAster.desktop || die "invalid Linux package asset"
			goos=linux
			binary_path="$package_root/payload/goaster"
			mkdir -p "$(dirname "$binary_path")"
			cp "$script_dir/linux/GoAster.desktop" "$package_root/GoAster.desktop"
			chmod 755 "$package_root/GoAster.desktop"
			archive_format=tar.gz
			archive_extension=tar.gz
			binary_suffix=
			;;
		*)
			die "unknown platform: $platform"
			;;
	esac

	# Skill and memory templates are ordinary files alongside the payload.
	copy_starters "$(dirname "$binary_path")/skills"

	case "$architecture" in
		amd64|arm64) ;;
		*) die "unsupported architecture: $architecture (use amd64 or arm64)" ;;
	esac

	printf 'Building %s/%s package\n' "$platform" "$architecture"
	if ! (cd "$repo_root" && GOOS="$goos" GOARCH="$architecture" CGO_ENABLED=0 \
		go build -trimpath -o "$binary_path$binary_suffix" ./cmd/goaster); then
		die "could not build $platform/$architecture GoAster payload"
	fi

	if [ -e "$package_dir" ] || [ -L "$package_dir" ]; then
		rm -rf "$package_dir"
	fi
	mv "$package_root" "$package_dir"
	archive_package "$archive_format" "$archive_extension" "$package_dir"
	stage_dir=
}

build_platform() {
	platform=$1
	default_architecture=$2
	if [ -n "${GOASTER_ARCH:-}" ]; then
		build_package "$platform" "$GOASTER_ARCH"
	else
		case "$platform" in
			macos)
				build_package "$platform" arm64
				build_package "$platform" amd64
				;;
			*) build_package "$platform" "$default_architecture" ;;
		esac
	fi
}

case "$target" in
	all)
		build_platform macos arm64
		build_platform windows amd64
		build_platform linux amd64
		;;
	macos) build_platform macos arm64 ;;
	windows) build_platform windows amd64 ;;
	linux) build_platform linux amd64 ;;
	*)
		usage >&2
		exit 2
		;;
esac

printf 'Packages written to %s\n' "$output_dir"

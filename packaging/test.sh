#!/bin/sh
# Offline package and fresh-install integration checks. The user's Git index is unchanged.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
. "$script_dir/assets.sh"
test_dir=$(mktemp -d "$repo_root/.packaging-test.XXXXXX")
trap 'rm -rf "$test_dir"' 0 1 2 3 15
mkdir -p "$test_dir/tmp" "$test_dir/cache"
TMPDIR="$test_dir/tmp"
GOCACHE=${GOCACHE:-"$test_dir/cache"}
GOPROXY=off
GOSUMDB=off
GOTOOLCHAIN=local
GIT_INDEX_FILE="$test_dir/index"
export TMPDIR GOCACHE GOPROXY GOSUMDB GOTOOLCHAIN GIT_INDEX_FILE

fail() {
	printf 'packaging test: %s\n' "$*" >&2
	exit 1
}

cd "$repo_root"
git read-tree HEAD
# The working tree may contain a new intended asset that the user has not yet
# staged. Give this isolated index the same entry a future commit would have.
memory_hash=$(git hash-object skills/MEMORY.md)
git update-index --add --cacheinfo "100644,$memory_hash,skills/MEMORY.md"
assets_hash=$(git hash-object packaging/assets.sh)
git update-index --add --cacheinfo "100644,$assets_hash,packaging/assets.sh"
check_starter_tree "$repo_root/skills"
check_tracked_starters

fixture="$test_dir/assets"
cp -R "$repo_root/skills" "$fixture"
for extra in "$fixture/.unexpected" "$fixture/project-memory/.unexpected"; do
	printf 'unexpected\n' >"$extra"
	if check_starter_tree "$fixture" >/dev/null 2>&1; then
		fail "unexpected asset was accepted: $extra"
	fi
	rm "$extra"
done
rm "$fixture/project-memory/AUTH.md"
ln -s SKILL.md "$fixture/project-memory/AUTH.md"
if check_starter_tree "$fixture" >/dev/null 2>&1; then
	fail 'symbolic-link asset was accepted'
fi
mkdir "$test_dir/existing-assets"
printf 'existing user asset\n' >"$test_dir/existing-assets/MEMORY.md"
if copy_starters "$test_dir/existing-assets" >"$test_dir/existing-assets.log" 2>&1; then
	fail 'starter copy accepted an existing destination'
fi
if [ "$(cat "$test_dir/existing-assets/MEMORY.md")" != 'existing user asset' ] || [ "$(find "$test_dir/existing-assets" -mindepth 1 | wc -l | tr -d ' ')" != 1 ]; then
	fail 'starter copy modified an existing asset directory'
fi

mkdir "$test_dir/archive-source"
mkfifo "$test_dir/archive-source/unexpected-fifo"
if go run ./packaging/cmd/archive --format tar.gz --source "$test_dir/archive-source" --output "$test_dir/bad.tar.gz" >"$test_dir/archive.log" 2>&1; then
	fail 'archive helper accepted a non-regular asset'
fi

git update-index --force-remove skills/MEMORY.md
if ./packaging/build.sh linux "$test_dir/should-not-build" >"$test_dir/rejected.log" 2>&1; then
	fail 'build accepted an untracked required starter asset'
fi
git update-index --add --cacheinfo "120000,$memory_hash,skills/MEMORY.md"
if check_tracked_starters >"$test_dir/symlink-index.log" 2>&1; then
	fail 'tracked symbolic-link entry was accepted as a regular asset'
fi
git update-index --add --cacheinfo "100644,$memory_hash,skills/MEMORY.md"

host_os=$(go env GOHOSTOS)
host_arch=$(go env GOHOSTARCH)
case "$host_arch" in amd64|arm64) ;; *) fail "unsupported test architecture: $host_arch" ;; esac
if [ "$host_arch" = amd64 ]; then
	./packaging/build.sh all "$test_dir/dist"
else
	GOASTER_ARCH="$host_arch" ./packaging/build.sh all "$test_dir/dist"
	GOASTER_ARCH=amd64 ./packaging/build.sh macos "$test_dir/dist"
fi
./packaging/validate.sh "$test_dir/dist"

# Extract the actual release archives and compare their starter layouts and
# contents with the package directories and the reviewed working tree.
python3 - "$test_dir/dist" "$test_dir/extracted" <<'PY'
import pathlib, sys, tarfile, zipfile
dist, out = map(pathlib.Path, sys.argv[1:])
out.mkdir()
for archive in dist.iterdir():
    if archive.name.endswith('.zip'):
        with zipfile.ZipFile(archive) as source:
            source.extractall(out)
    elif archive.name.endswith('.tar.gz'):
        with tarfile.open(archive, 'r:gz') as source:
            source.extractall(out)
PY
for package in "$test_dir/dist"/GoAster-*; do
	[ -d "$package" ] || continue
	name=${package##*/}
	case "$name" in
		GoAster-macos-*) relative=GoAster.app/Contents/Resources/skills ;;
		*) relative=payload/skills ;;
	esac
	diff -r "$repo_root/skills" "$package/$relative"
	diff -r "$package/$relative" "$test_dir/extracted/$name/$relative"
done

linux_package="$test_dir/dist/GoAster-linux-$host_arch"
printf 'unexpected\n' >"$linux_package/payload/skills/project-memory/.unexpected"
if ./packaging/validate.sh "$test_dir/dist" >"$test_dir/invalid-package.log" 2>&1; then
	fail 'validator accepted an unexpected packaged asset'
fi
rm "$linux_package/payload/skills/project-memory/.unexpected"

if [ "$host_os" != linux ]; then
	printf 'Archive layouts passed; executable installation checks require a Linux host.\n'
	exit 0
fi

binary="$test_dir/extracted/GoAster-linux-$host_arch/payload/goaster"
chmod 755 "$binary"
mkdir -p "$test_dir/hostile/skills" "$test_dir/fresh"
printf 'poison\n' >"$test_dir/hostile/skills/MEMORY.md"
printf 'not a credential\n' >"$test_dir/not-credential"
ln -s "$test_dir/not-credential" "$test_dir/auth-link"
mkdir -p "$test_dir/lone" "$test_dir/lone-home"
cp "$binary" "$test_dir/lone/goaster"
if (cd "$test_dir/hostile" && unset GOASTER_STARTER_DIR && \
	HOME="$test_dir/lone-home" USERPROFILE="$test_dir/lone-home" "$test_dir/lone/goaster" -auth-file "$test_dir/auth-link" >"$test_dir/lone.log" 2>&1); then
	fail 'unpackaged binary unexpectedly succeeded'
fi
if ! grep -F 'starter assets not found' "$test_dir/lone.log" >/dev/null; then
	cat "$test_dir/lone.log" >&2
	fail 'unpackaged binary accepted skills from the working directory'
fi
if (cd "$test_dir/hostile" && \
	GOASTER_STARTER_DIR="$linux_package/payload/skills" HOME="$test_dir/lone-home" USERPROFILE="$test_dir/lone-home" "$test_dir/lone/goaster" -auth-file "$test_dir/auth-link" >"$test_dir/override.log" 2>&1); then
	fail 'fixture authentication unexpectedly succeeded with override'
fi
if ! grep -F 'credential file must not be a symbolic link' "$test_dir/override.log" >/dev/null; then
	cat "$test_dir/override.log" >&2
	fail 'explicit starter override did not install the assets'
fi
cmp "$repo_root/skills/MEMORY.md" "$test_dir/lone-home/.goaster/artefacts/memory/MEMORY.md"
run_app() {
	app_home=$1
	if (cd "$test_dir/hostile" && unset GOASTER_STARTER_DIR && \
		HOME="$app_home" USERPROFILE="$app_home" "$binary" -auth-file "$test_dir/auth-link" >"$test_dir/app.log" 2>&1); then
		fail 'fixture authentication unexpectedly succeeded'
	fi
	if ! grep -F 'credential file must not be a symbolic link' "$test_dir/app.log" >/dev/null; then
		cat "$test_dir/app.log" >&2
		fail 'app did not complete starter installation before rejecting fixture authentication'
	fi
}

# Extract the documented host stage and run it in a new shell. Redirect only
# its fixed /usr base into the fixture; all other commands are the real stage.
remote_root="$test_dir/remote-usr"
mkdir -p "$remote_root/local/bin" "$test_dir/remote-home" "$test_dir/git-bin"
awk '
    /^For a first installation, include/ { selected = 1 }
    selected && /^```sh$/ { inside = 1; next }
    inside && /^```$/ { exit }
    inside { if ($0 == "ASSET_BASE=/usr") $0 = "ASSET_BASE=\"$TEST_ASSET_BASE\""; print }
' "$repo_root/skills/ovh-remote/SKILL.md" >"$test_dir/remote-stage.sh"
if ! grep -F 'install_starters_to_prefix ' "$test_dir/remote-stage.sh" >/dev/null ||
   ! grep -F 'ASSET_BASE="$TEST_ASSET_BASE"' "$test_dir/remote-stage.sh" >/dev/null; then
	fail 'could not extract and isolate the documented remote installation stage'
fi
REAL_GIT=$(command -v git)
SOURCE_DIR=$repo_root
SOURCE_COMMIT=$(git rev-parse HEAD)
TEST_ASSET_BASE=$remote_root
export REAL_GIT SOURCE_DIR SOURCE_COMMIT TEST_ASSET_BASE
# The test uses an isolated index and intentionally dirty working tree. Stub
# only the cleanliness result while all tracked-file and commit checks use Git.
cat >"$test_dir/git-bin/git" <<'SH'
#!/bin/sh
if [ "$#" -eq 4 ] && [ "$1" = -C ] && [ "$3" = status ] && [ "$4" = --porcelain ]; then
	case ${REMOTE_TEST_STATUS:-} in clean) exit 0 ;; dirty) printf ' M fixture\n'; exit 0 ;; esac
fi
exec "$REAL_GIT" "$@"
SH
chmod 755 "$test_dir/git-bin/git"
PATH="$test_dir/git-bin:$PATH"
export PATH
(
  repo_root=$SOURCE_DIR
  . "$SOURCE_DIR/packaging/assets.sh"
  check_starter_tree "$SOURCE_DIR/skills"
  check_tracked_starters
) # The next stage is a separate shell and cannot inherit these functions.
if (unset repo_root; REMOTE_TEST_STATUS=dirty sh -eu "$test_dir/remote-stage.sh") >"$test_dir/remote-dirty.log" 2>&1; then
	fail 'remote installation accepted a dirty checkout'
fi
if (unset repo_root; SOURCE_COMMIT=invalid REMOTE_TEST_STATUS=clean sh -eu "$test_dir/remote-stage.sh") >"$test_dir/remote-commit.log" 2>&1; then
	fail 'remote installation accepted a different source commit'
fi
sed '/^repo_root="\$SOURCE_DIR"$/d' "$test_dir/remote-stage.sh" >"$test_dir/remote-no-root.sh"
sed '/^\. "\$SOURCE_DIR\/packaging\/assets.sh"$/d' "$test_dir/remote-stage.sh" >"$test_dir/remote-no-helper.sh"
for broken_stage in "$test_dir/remote-no-root.sh" "$test_dir/remote-no-helper.sh"; do
	if (unset repo_root; REMOTE_TEST_STATUS=clean sh -eu "$broken_stage") >"$test_dir/broken-stage.log" 2>&1; then
		fail "remote installation succeeded without its own checkout or helper: $broken_stage"
	fi
done
if [ -e "$remote_root/local/share" ]; then
	fail 'remote installation touched assets before source validation'
fi
go build -trimpath -o "$remote_root/local/bin/goaster-agent" ./cmd/goaster-agent
(unset repo_root; REMOTE_TEST_STATUS=clean sh -eu "$test_dir/remote-stage.sh")
if (unset repo_root; REMOTE_TEST_STATUS=clean sh -eu "$test_dir/remote-stage.sh") >"$test_dir/existing-prefix.log" 2>&1; then
	fail 'remote asset installation replaced an existing directory'
fi
python3 - "$remote_root/local/bin/goaster-agent" "$test_dir/remote-home" "$test_dir/hostile" "$test_dir/remote-agent.log" <<'PY'
import os, pathlib, subprocess, sys, time
binary, home, working, log_path = sys.argv[1:]
env = os.environ.copy()
env.pop('GOASTER_STARTER_DIR', None)
env.update(HOME=home, USERPROFILE=home)
root = pathlib.Path(home) / '.goaster' / 'artefacts'
with open(log_path, 'w') as log:
    agent = subprocess.Popen([binary], cwd=working, env=env, stdout=log, stderr=log)
    try:
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            if (root / 'skills' / 'project-memory' / 'AUTH.md').is_file() and (root / 'memory' / 'MEMORY.md').is_file() and not (root / '.skills-installing').exists():
                break
            if agent.poll() is not None:
                raise SystemExit('remote agent exited before installing assets')
            time.sleep(0.05)
        else:
            raise SystemExit('remote agent did not complete its first installation')
    finally:
        if agent.poll() is None:
            agent.terminate()
            agent.wait(timeout=10)
PY
for skill in $starter_skills; do
	diff -r "$repo_root/skills/$skill" "$test_dir/remote-home/.goaster/artefacts/skills/$skill"
done
cmp "$repo_root/skills/MEMORY.md" "$test_dir/remote-home/.goaster/artefacts/memory/MEMORY.md"
python3 - "$test_dir/remote-home/.goaster/artefacts" <<'PY'
import pathlib, stat, sys
root = pathlib.Path(sys.argv[1])
for directory in [root / 'skills', root / 'memory', *(root / 'skills').iterdir()]:
    if stat.S_IMODE(directory.stat().st_mode) != 0o700:
        raise SystemExit(f'unsafe installed directory permissions: {directory}')
for path in (root / 'skills').glob('*/*'):
    if stat.S_IMODE(path.stat().st_mode) != 0o600:
        raise SystemExit(f'unsafe installed skill permissions: {path}')
if stat.S_IMODE((root / 'memory' / 'MEMORY.md').stat().st_mode) != 0o600:
    raise SystemExit('unsafe default memory permissions')
PY

sh "$script_dir/test-remote-assets.sh"

run_app "$test_dir/fresh"
diff -r "$repo_root/skills/project-memory" "$test_dir/fresh/.goaster/artefacts/skills/project-memory"
cmp "$repo_root/skills/MEMORY.md" "$test_dir/fresh/.goaster/artefacts/memory/MEMORY.md"

printf 'Offline package and fresh local and remote installation checks passed.\n'

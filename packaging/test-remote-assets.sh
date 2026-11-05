#!/bin/sh
# Exercise the documented fresh remote service binding without opening a socket.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
test_dir=$(mktemp -d "$repo_root/.remote-asset-test.XXXXXX")
trap 'rm -rf "$test_dir"' 0 1 2 3 15
mkdir -p "$test_dir/usr/local/bin"

# Build the real agent startup with only its listener disabled through a Go
# build overlay. It installs assets and exits without opening a socket.
python3 - "$repo_root" "$test_dir" <<'PY'
import json
import pathlib
import sys

repo, fixture = map(pathlib.Path, sys.argv[1:])
source = repo / 'cmd' / 'goaster-agent' / 'main.go'
contents = source.read_text()
listener = '\tlistener, err := controltransport.Listen()\n'
end_marker = '\n}\n\nfunc handlerWithMemoryAndRegistryAndGitHubAuthWithSecret'
if contents.count(listener) != 1 or contents.count(end_marker) != 1:
    raise SystemExit('could not isolate the agent listener for the offline test')
start = contents.index(listener)
end = contents.index(end_marker, start)
if 'server.Serve(listener)' not in contents[start:end] or 'server.Close()' not in contents[start:end]:
    raise SystemExit('could not isolate the agent listener for the offline test')
overlay = fixture / 'agent-main.go'
overlay.write_text(contents[:start] + '\t_, _, _, _ = ctx, controlSecret, memory, registry\n\treturn nil' + contents[end:])
(fixture / 'overlay.json').write_text(json.dumps({'Replace': {str(source): str(overlay)}}))
PY
cd "$repo_root"
GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local CGO_ENABLED=0 \
	go build -overlay "$test_dir/overlay.json" -o "$test_dir/usr/local/bin/goaster-agent" ./cmd/goaster-agent

python3 - "$repo_root" "$test_dir" <<'PY'
import pathlib
import shlex
import shutil
import subprocess
import sys

repo, fixture = map(pathlib.Path, sys.argv[1:])
skill = (repo / 'skills' / 'ovh-remote' / 'SKILL.md').read_text()
heading = 'For a fresh systemd agent unit, include these service settings:'
if skill.count(heading) != 1:
    raise SystemExit('fresh agent service settings are missing or ambiguous')
section = skill.split(heading, 1)[1]
if not section.startswith('\n\n```ini\n') or '\n```' not in section:
    raise SystemExit('fresh agent service settings do not have an ini block')
unit = section.split('```ini\n', 1)[1].split('\n```', 1)[0].splitlines()
if unit.count('User=root') != 1:
    raise SystemExit('fresh agent unit does not specify root')
exec_lines = [line[len('ExecStart='):] for line in unit if line.startswith('ExecStart=')]
if len(exec_lines) != 1:
    raise SystemExit('fresh agent unit needs exactly one launch command')
command = shlex.split(exec_lines[0])
if command != ['/usr/bin/env', 'HOME=/root', 'GOASTER_STARTER_DIR=/usr/local/share/goaster/skills', '/usr/local/bin/goaster-agent']:
    raise SystemExit('fresh agent unit does not directly bind the reviewed asset path')

binary = fixture / 'usr' / 'local' / 'bin' / 'goaster-agent'
reviewed = fixture / 'usr' / 'local' / 'share' / 'goaster' / 'skills'
shadow = binary.parent / 'skills'
reviewed.parent.mkdir(parents=True)
shutil.copytree(repo / 'skills', reviewed)
shutil.copytree(reviewed, shadow)
(shadow / 'MEMORY.md').write_text('shadow memory\n')
(shadow / 'ovh-remote' / 'SKILL.md').write_text('shadow workflow\n')
(shadow / 'ovh-remote' / 'AUTH.md').write_text('shadow authorization\n')
working = fixture / 'working'
working.mkdir()

def run(home, bind):
    home.mkdir()
    env = {'HOME': str(home), 'USERPROFILE': str(home)}
    if bind:
        # Simulate a conflicting manager or EnvironmentFile value as well as the
        # conflicting executable-adjacent skills directory.
        env['GOASTER_STARTER_DIR'] = str(shadow)
        substitutions = {
            'HOME=/root': 'HOME=' + str(home),
            'GOASTER_STARTER_DIR=/usr/local/share/goaster/skills': 'GOASTER_STARTER_DIR=' + str(reviewed),
            '/usr/local/bin/goaster-agent': str(binary),
        }
        args = [substitutions.get(arg, arg) for arg in command]
    else:
        args = [str(binary)]
    return subprocess.run(args, cwd=working, env=env, text=True, capture_output=True)

def installed(home):
    return home / '.goaster' / 'artefacts'

# Prove the collision is meaningful: without the service binding the normal
# executable-relative discovery selects the conflicting tree.
unbound_home = fixture / 'unbound-home'
result = run(unbound_home, False)
if result.returncode or (installed(unbound_home) / 'memory' / 'MEMORY.md').read_text() != 'shadow memory\n':
    raise SystemExit('fixture did not demonstrate executable-adjacent shadowing')

bound_home = fixture / 'bound-home'
result = run(bound_home, True)
if result.returncode:
    raise SystemExit('bound fresh installation failed: ' + result.stderr)
destination = installed(bound_home)
expected = {p.relative_to(reviewed): p.read_bytes() for p in reviewed.rglob('*') if p.is_file() and p.name != 'MEMORY.md'}
actual = {p.relative_to(destination / 'skills'): p.read_bytes() for p in (destination / 'skills').rglob('*') if p.is_file()}
if actual != expected or (destination / 'memory' / 'MEMORY.md').read_bytes() != (reviewed / 'MEMORY.md').read_bytes():
    raise SystemExit('bound installation did not copy exactly the reviewed skills and memory')

# A missing or symlinked explicit source must fail closed, despite the valid
# conflicting tree and inherited override.
reviewed.rename(reviewed.with_name('reviewed-backup'))
for name in ('missing', 'symlink'):
    if name == 'symlink':
        reviewed.symlink_to(shadow, target_is_directory=True)
    home = fixture / (name + '-home')
    result = run(home, True)
    if result.returncode == 0 or 'starter assets directory is unavailable or not a real directory' not in result.stderr:
        raise SystemExit(name + ' reviewed source did not fail closed')
    root = installed(home)
    if (root / 'skills').exists() or (root / 'memory' / 'MEMORY.md').exists():
        raise SystemExit(name + ' reviewed source installed unexpected assets')

print('Fresh remote service asset binding checks passed offline.')
PY

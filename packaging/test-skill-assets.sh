#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
. "$script_dir/assets.sh"

expected_starter_skills='background-command-administration bitwarden-secrets-manager github-auth goaster-backup goaster-build-test goaster-source-code-evolution goaster-deploy goaster-env goaster-find-contributions goaster-fork goaster-migration goaster-share-contribution hetzner-auth hetzner-auto-backup manage-skills modify-goaster-source-code internet-fetch model-administration project-memory read-write-temp-files ripgrep-search memory-consolidation goaster-telegram-groups-best-practices telegram-commands'
if [ "$starter_skills" != "$expected_starter_skills" ]; then
	printf 'starter skill allowlist changed unexpectedly\nexpected: %s\nactual: %s\n' \
		"$expected_starter_skills" "$starter_skills" >&2
	exit 1
fi

check_starter_tree "$repo_root/skills"
check_tracked_starters

python3 - "$repo_root" "$expected_starter_skills" <<'PY'
import pathlib
import re
import sys
from urllib.parse import unquote, urlsplit

root = pathlib.Path(sys.argv[1]).resolve()
expected = sys.argv[2].split()
actual = sorted(
    path.name
    for path in (root / "skills").iterdir()
    if path.name != "MEMORY.md"
)
if actual != sorted(expected):
    raise SystemExit(f"skill directory inventory differs: {actual!r}")

for name in expected:
    directory = root / "skills" / name
    if not directory.is_dir() or directory.is_symlink():
        raise SystemExit(f"invalid skill directory: {directory}")
    metadata = directory / "META.md"
    lines = metadata.read_text(encoding="utf-8").splitlines()
    if len(lines) != 2 or not lines[0].startswith("name: ") or not lines[1].startswith("description: "):
        raise SystemExit(f"invalid META.md shape: {metadata}")
    if lines[0] != f"name: {name}":
        raise SystemExit(f"META.md name does not match directory: {metadata}")
    for filename in ("META.md", "SKILL.md", "AUTH.md"):
        path = directory / filename
        if not path.is_file() or path.is_symlink():
            raise SystemExit(f"invalid starter file: {path}")
        path.read_text(encoding="utf-8")

workflow = (root / "skills" / "hetzner-auto-backup" / "SKILL.md").read_text(encoding="utf-8")
for phrase in (
    "goaster-backup",
    "POST /servers/{id}/actions/enable_backup",
    "GET /servers/{id}/actions/{action_id}",
    "explicitly approve",
    "recurring backup charge",
    "daily automatic",
    "seven backup slots",
    "application-consistent",
    "Attached Volumes are not included",
    "If backups are already enabled",
    "stop and report",
):
    if phrase not in workflow:
        raise SystemExit(f"hetzner-auto-backup workflow missing required guidance: {phrase!r}")

migration = (root / "skills" / "goaster-migration" / "SKILL.md").read_text(encoding="utf-8")
migration_text = " ".join(migration.split())
for phrase in (
    "Hard blocker: GoAsterWeb handoff is not available",
    "Current capability boundary",
    "no GoAsterWeb handoff endpoint",
    "must not:",
    "register a migration handoff",
    "schedule or run an availability monitor",
    "create, reserve, allocate",
    "Approved availability monitor: future policy only",
    "approved monitor",
    "30-minute recurring check",
    "Private owner channel and key handling",
    "one-to-one private chat",
    "distinct bot tokens",
    "Pair the new bot independently",
    "manual and agent-guided",
    "private-key delivery capability",
    "Initial pairing requires a private Telegram chat",
    "ordinary conversations and some commands can also be used in groups",
    "must not be used to carry a private key",
    "absolute seven-day (7-day) expiry",
    "Errors and manual reconciliation",
    "unknown result",
    "No automatic deletion applies",
    "30-minute availability cadence",
    "No backup or snapshot is required for this manual-move",
    "manual",
    "Never delete automatically",
):
    if phrase not in migration_text:
        raise SystemExit(f"goaster-migration workflow missing required guidance: {phrase!r}")

hard_blocker = migration.index("## Hard blocker:")
future_policy = migration.index("## Future approved orchestration")
if hard_blocker > future_policy:
    raise SystemExit("migration hard blocker appears after future orchestration policy")

current_policy = migration[hard_blocker:future_policy]
current_policy_text = " ".join(current_policy.split())
for phrase in (
    "must not schedule, run, emulate, or claim to have run one",
    "cannot generate, carry, verify, or deliver it",
    "No automatic deletion applies",
):
    if phrase not in current_policy_text:
        raise SystemExit(f"migration current policy lost blocked safeguard: {phrase!r}")

future_policy_text = migration[future_policy:]
future_policy_text = " ".join(future_policy_text.split())
for phrase in (
    "disables any approved monitor",
    "never blindly retries or creates a duplicate",
    "never retries or deletes automatically",
    "distinct Telegram bot tokens",
    "owner chooses when to stop the old agent",
):
    if phrase not in future_policy_text:
        raise SystemExit(f"migration future policy missing safeguard: {phrase!r}")

# Check local Markdown links so renamed skills and stale skill paths fail in
# the same offline test as the package allowlist.
link_pattern = re.compile(r"\]\((?:<([^>]+)>|([^\s)]+))")
for markdown in root.rglob("*.md"):
    if ".git" in markdown.parts or "dist" in markdown.parts:
        continue
    text = markdown.read_text(encoding="utf-8")
    for match in link_pattern.finditer(text):
        target = unquote(match.group(1) or match.group(2))
        parsed = urlsplit(target)
        if parsed.scheme or parsed.netloc or not parsed.path or parsed.path == "URL":
            continue
        candidate = (markdown.parent / parsed.path).resolve()
        if not candidate.exists():
            raise SystemExit(f"stale local Markdown link in {markdown}: {target}")

print("Skill allowlist and local Markdown reference checks passed offline.")
PY

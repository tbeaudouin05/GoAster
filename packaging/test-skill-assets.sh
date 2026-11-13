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

package_root=$(mktemp -d)
trap 'rm -rf "$package_root"' EXIT HUP INT TERM
mkdir "$package_root/share"
install_starters_to_prefix "$package_root/share"

python3 - "$repo_root" "$expected_starter_skills" "$package_root/share" <<'PY'
import pathlib
import re
import sys
from urllib.parse import unquote, urlsplit

root = pathlib.Path(sys.argv[1]).resolve()
expected = sys.argv[2].split()
installed_root = pathlib.Path(sys.argv[3]).resolve()
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
    "registered `goaster_migration` tool",
    "`quote`",
    "`start`",
    "`status`",
    "`stop`",
    "`repair_delivery`",
    "callback-only cloud-init",
    "no second create prompt",
    "30-minute monitor",
    "private user",
    "manual repair",
    "HCLOUD_TOKEN",
    "no Telegram command",
    "no callback polling",
    "Never retry a",
    "GoAsterWeb setup",
    "fixed GoAsterWeb origin",
    "correct provider token and project",
    "Hetzner project",
    "one-create",
    "protected bounded handoff",
    "distinct new BotFather bot",
):
    if phrase not in migration_text:
        raise SystemExit(f"goaster-migration workflow missing required guidance: {phrase!r}")

for obsolete_phrase in (
    "pure skill workflow",
    "shell_execute",
    "RunLauncher",
    "RunCommand",
    "inline Python",
    "/get_migration",
):
    if obsolete_phrase in migration_text:
        raise SystemExit(f"goaster-migration workflow retained obsolete blocker/policy: {obsolete_phrase!r}")

if not (root / "internal" / "migration").is_dir():
    raise SystemExit("dedicated internal/migration package is missing")
main_source = (root / "cmd" / "goaster-telegram" / "main.go").read_text(encoding="utf-8")
for forbidden in ("goaster-telegram migration", "RunLauncher", "RunCommand", "/get_migration", "internal/migration"):
    if forbidden in main_source:
        raise SystemExit(f"goaster-telegram retained migration command/CLI reference: {forbidden!r}")

installed_migration = (installed_root / "skills" / "goaster-migration" / "SKILL.md").read_text(encoding="utf-8")
for forbidden in ("GoAsterWeb/", "../../docs/", "../docs/", "migration-handoff-20261112"):
    if forbidden in installed_migration:
        raise SystemExit(f"installed migration skill retained a local checkout/document dependency: {forbidden!r}")

notes = (root / "docs" / "goaster-migration.md").read_text(encoding="utf-8")
notes_text = " ".join(notes.split())
for phrase in (
    "in-process migration tool",
    "`goaster_migration`",
    "every 1800 seconds",
    "POST /migration/register",
    "POST /migration/host-key",
    "110%",
    "primary-IP",
    "delivery_pending",
    "repair_delivery",
    "manual-reconciliation terminal",
    "no automatic expiry-cleanup job",
    "no second create prompt",
    "Do not ask for or verify the Hetzner project",
    "no Telegram command",
    "distinct new BotFather bot",
    "locally paired",
    "No automatic resend",
    "automatic recovery",
    "synthetic/local test seams",
):
    if phrase not in notes_text:
        raise SystemExit(f"migration task notes missing required scope/contract: {phrase!r}")

canonical_origin = "https://goasterweb-thomas.fly.dev"
canonical_service_url = canonical_origin + "/"
public_service_policy = (
    f"`{canonical_origin}`",
    "Do not ask for another origin",
    "Do not ask for or verify the Hetzner project",
    "Never retry a possibly completed create",
)
for label, content in (("skill", migration), ("notes", notes)):
    normalized = " ".join(content.split())
    for phrase in public_service_policy:
        if phrase not in normalized:
            raise SystemExit(f"migration {label} missing public-service policy: {phrase!r}")
    for forbidden in (
        "${GOASTERWEB_ORIGIN}",
        "https://<origin>/migration",
        "What is the public HTTPS address of your deployed GoAsterWeb service?",
    ):
        if forbidden in content:
            raise SystemExit(f"migration {label} retained an origin question/placeholder: {forbidden!r}")

callback_url = "https://goasterweb-thomas.fly.dev/migration/host-key"
if callback_url not in notes:
    raise SystemExit("migration notes callback does not use the canonical HTTPS service")

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

for markdown in installed_root.rglob("*.md"):
    text = markdown.read_text(encoding="utf-8")
    for match in link_pattern.finditer(text):
        target = unquote(match.group(1) or match.group(2))
        parsed = urlsplit(target)
        if parsed.scheme or parsed.netloc or not parsed.path or parsed.path == "URL":
            continue
        candidate = (markdown.parent / parsed.path).resolve()
        if not candidate.exists():
            raise SystemExit(f"stale installed Markdown link in {markdown}: {target}")

print("Skill allowlist, installed package, and local Markdown reference checks passed offline.")
PY

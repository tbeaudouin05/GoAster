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
    "pure skill workflow",
    "existing `shell_execute`, `cron`, and authenticated Hetzner/provider tools",
    "no migration executable",
    "callback-only cloud-init",
    "securely record the private SSH key (login key) and provider server ID",
    "does not install, build, configure, start, deploy, or transfer GoAster",
    "destination SSH",
    "informed approval",
    "exactly one create",
    "no second create prompt",
    "every 30 minutes, 24/7",
    "goaster-migration-<first 16 lowercase hex characters of attempt>",
    "goaster-migration-attempt=<attempt>",
    "/migration/register",
    "callback authorization only",
    "not host handoff recording",
    "/migration/host-key",
    "records this host-key handoff in Redis",
    "absolute maximum of seven days",
    "There is no cancellation route",
    "unsafe-status escape",
    "automatic recovery",
    "distinct new BotFather bot",
    "Never copy or reuse the old bot token",
    "old and new agents may coexist",
    "fragment-only URL",
    "exact local envelope",
    "at most 32 KiB",
    "owner manually removes the file after delivery or abandonment",
    "inline Python",
    "without following symlinks",
    "duplicate or extra JSON fields",
    "locally paired user ID",
    "does not wait for or claim to observe the later guest callback",
    "no new callback polling",
    "Do not add a command, tool, script, or runtime helper",
    "Never blindly repeat a create",
    "manually identify the unwanted new server",
    "manually delete it if appropriate",
    "start over with a new attempt",
    "Do not invent a helper",
):
    if phrase not in migration_text:
        raise SystemExit(f"goaster-migration workflow missing required guidance: {phrase!r}")

for obsolete_phrase in (
    "goaster-telegram migration",
    "RunLauncher",
    "RunCommand",
    "migration helper",
    "migration monitor implementation",
    "/get_migration",
):
    if obsolete_phrase in migration_text:
        raise SystemExit(f"goaster-migration workflow retained obsolete blocker/policy: {obsolete_phrase!r}")

if (root / "internal" / "migration").exists():
    raise SystemExit("removed internal/migration provisioning package is still present")
main_source = (root / "cmd" / "goaster-telegram" / "main.go").read_text(encoding="utf-8")
for forbidden in ("goaster-telegram migration", "RunLauncher", "internal/migration"):
    if forbidden in main_source:
        raise SystemExit(f"goaster-telegram retained removed migration CLI reference: {forbidden!r}")

installed_migration = (installed_root / "skills" / "goaster-migration" / "SKILL.md").read_text(encoding="utf-8")
for forbidden in ("GoAsterWeb/", "../../docs/", "../docs/", "migration-handoff-20261112"):
    if forbidden in installed_migration:
        raise SystemExit(f"installed migration skill retained a local checkout/document dependency: {forbidden!r}")

notes = (root / "docs" / "goaster-migration.md").read_text(encoding="utf-8")
notes_text = " ".join(notes.split())
for phrase in (
    "pure skill",
    "GoAster-side responsibility",
    "exactly one explicitly approved Hetzner server",
    "POST /migration/register",
    "callback authorization, not host handoff recording",
    "POST /migration/host-key",
    "goaster-migration-attempt=<attempt>",
    "records this host-key handoff in Redis",
    "expiry-only with a maximum of seven days",
    "distinct new BotFather bot",
    "Do not copy or reuse the old bot token",
    "old and new agents may coexist",
    "exactly this JSON envelope shape",
    "bounded to 32 KiB",
    "owner manually removes it after delivery",
    "inline Python",
    "locally paired user",
    "The GoAster runtime does not read or send this file",
    "without weakening permissions",
    "no new callback polling",
    "There is no cancellation route",
    "30-minute, 24/7 monitor",
    "Never blindly repeat a create",
    "manually deletes the unwanted server",
    "No live provider request",
):
    if phrase not in notes_text:
        raise SystemExit(f"migration task notes missing required scope/contract: {phrase!r}")

canonical_origin = "https://goasterweb-thomas.fly.dev"
canonical_service_url = canonical_origin + "/"
public_service_policy = (
    f"canonical origin is `{canonical_origin}` with no trailing slash",
    f"service URL is `{canonical_service_url}` with a trailing slash",
    "Resolve every GoAsterWeb `/migration/...` endpoint against this canonical service",
    "Never ask whether GoAsterWeb has been deployed or request its public HTTPS address.",
    "Do not add a readiness question or reachability preflight.",
    "Assume the public service is available on the normal path",
    "if an actual HTTPS request fails, stop and report a safe failure.",
    "Preserve TLS verification",
    "never blindly retry a request whose outcome is uncertain.",
)
for label, content in (("skill", migration), ("notes", notes)):
    normalized = " ".join(content.split())
    for phrase in public_service_policy:
        if phrase not in normalized:
            raise SystemExit(f"migration {label} missing public-service policy: {phrase!r}")
    for forbidden in (
        "${GOASTERWEB_ORIGIN}",
        "https://<origin>/migration",
        "Do you have a deployed GoAsterWeb service?",
        "What is the public HTTPS address of your deployed GoAsterWeb service?",
    ):
        if forbidden in content:
            raise SystemExit(f"migration {label} retained an origin question/placeholder: {forbidden!r}")

callback_url = "https://goasterweb-thomas.fly.dev/migration/host-key"
if f'url = "{callback_url}"' not in migration:
    raise SystemExit("goaster-migration callback does not use the canonical HTTPS service")
if callback_url not in notes:
    raise SystemExit("migration notes callback does not use the canonical HTTPS service")
owner_url_prefix = "https://goasterweb-thomas.fly.dev/migration#attempt="
if owner_url_prefix not in migration or owner_url_prefix not in notes:
    raise SystemExit("migration owner URL does not use the canonical HTTPS service")

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

#!/bin/sh
# Shared allowlist for starter assets in release packages.
starter_skills='background-command-administration bitwarden-secrets-manager github-cli goaster-backup goaster-build-test goaster-coordinator goaster-deploy goaster-find-contributions goaster-fork goaster-share-contribution goaster-source internet-fetch model-administration ovh-remote ovh project-memory ripgrep-search self-improvement-memory-consolidation telegram-group-privacy'

check_starter_tree() {
	asset_tree=$1
	if [ ! -d "$asset_tree" ] || [ -L "$asset_tree" ]; then
		printf 'starter assets must be a real directory: %s\n' "$asset_tree" >&2
		return 1
	fi
	for asset_entry in "$asset_tree"/* "$asset_tree"/.[!.]* "$asset_tree"/..?*; do
		[ -e "$asset_entry" ] || [ -L "$asset_entry" ] || continue
		asset_name=${asset_entry##*/}
		case " $starter_skills " in
			*" $asset_name "*)
				if [ ! -d "$asset_entry" ] || [ -L "$asset_entry" ]; then
					printf 'starter skill must be a real directory: %s\n' "$asset_entry" >&2
					return 1
				fi
				for asset_child in "$asset_entry"/* "$asset_entry"/.[!.]* "$asset_entry"/..?*; do
					[ -e "$asset_child" ] || [ -L "$asset_child" ] || continue
					case ${asset_child##*/} in
						META.md|SKILL.md|AUTH.md) ;;
						*) printf 'unexpected starter asset: %s\n' "$asset_child" >&2; return 1 ;;
					esac
					if [ ! -f "$asset_child" ] || [ -L "$asset_child" ]; then
						printf 'starter asset must be a regular file: %s\n' "$asset_child" >&2
						return 1
					fi
				done
				;;
			*)
				if [ "$asset_name" != MEMORY.md ] || [ ! -f "$asset_entry" ] || [ -L "$asset_entry" ]; then
					printf 'unexpected starter asset: %s\n' "$asset_entry" >&2
					return 1
				fi
				;;
		esac
	done
	if [ ! -f "$asset_tree/MEMORY.md" ] || [ -L "$asset_tree/MEMORY.md" ]; then
		printf 'missing regular starter asset: %s/MEMORY.md\n' "$asset_tree" >&2
		return 1
	fi
	for asset_skill in $starter_skills; do
		if [ ! -d "$asset_tree/$asset_skill" ] || [ -L "$asset_tree/$asset_skill" ]; then
			printf 'missing starter skill directory: %s/%s\n' "$asset_tree" "$asset_skill" >&2
			return 1
		fi
		for asset_file in META.md SKILL.md AUTH.md; do
			if [ ! -f "$asset_tree/$asset_skill/$asset_file" ] || [ -L "$asset_tree/$asset_skill/$asset_file" ]; then
				printf 'missing regular starter asset: %s/%s/%s\n' "$asset_tree" "$asset_skill" "$asset_file" >&2
				return 1
			fi
		done
	done
}

check_tracked_regular() {
	tracked_path=$1
	if [ ! -f "$repo_root/$tracked_path" ] || [ -L "$repo_root/$tracked_path" ]; then
		printf 'asset must be a regular file: %s\n' "$tracked_path" >&2
		return 1
	fi
	if ! git -C "$repo_root" ls-files --stage -- "$tracked_path" |
		awk '$1 ~ /^100(644|755)$/ && $3 == "0" {count++} END {exit count != 1}'; then
		printf 'asset must be a tracked regular file: %s\n' "$tracked_path" >&2
		return 1
	fi
}

check_tracked_starters() {
	for tracked_asset in skills/MEMORY.md $(for tracked_skill in $starter_skills; do
		for tracked_file in META.md SKILL.md AUTH.md; do
			printf 'skills/%s/%s\n' "$tracked_skill" "$tracked_file"
		done
	done); do
		check_tracked_regular "$tracked_asset" || return 1
	done
}

copy_starters() {
	asset_destination=$1
	if ! mkdir "$asset_destination"; then
		printf 'starter asset destination must not already exist: %s\n' "$asset_destination" >&2
		return 1
	fi
	cp "$repo_root/skills/MEMORY.md" "$asset_destination/MEMORY.md" || return 1
	for asset_skill in $starter_skills; do
		mkdir "$asset_destination/$asset_skill" || return 1
		for asset_file in META.md SKILL.md AUTH.md; do
			cp "$repo_root/skills/$asset_skill/$asset_file" "$asset_destination/$asset_skill/$asset_file" || return 1
		done
	done
}

# Install into an already inspected share/goaster directory. The exclusive
# directory creation in copy_starters prevents replacement of existing assets.
install_starters_to_prefix() {
	asset_prefix=$1
	if [ ! -d "$asset_prefix" ] || [ -L "$asset_prefix" ]; then
		printf 'starter prefix must be a real directory: %s\n' "$asset_prefix" >&2
		return 1
	fi
	copy_starters "$asset_prefix/skills" || return 1
	check_starter_tree "$asset_prefix/skills" || return 1
	cmp "$repo_root/skills/MEMORY.md" "$asset_prefix/skills/MEMORY.md" || return 1
	for asset_skill in $starter_skills; do
		for asset_file in META.md SKILL.md AUTH.md; do
			cmp "$repo_root/skills/$asset_skill/$asset_file" "$asset_prefix/skills/$asset_skill/$asset_file" || return 1
		done
	done
}

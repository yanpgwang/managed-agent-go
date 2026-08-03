#!/usr/bin/env bash

set -euo pipefail

readonly SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly REPO_ROOT="$(cd -- "$SCRIPT_DIR/.." && pwd)"
readonly CATALOG_URL="https://platform.claude.com/llms.txt"
readonly DOCS_BASE_URL="https://platform.claude.com/docs/en"
readonly DEST_DIR="$REPO_ROOT/docs/_upstream/claude-managed-agents/snapshot"

stage_parent="${TMPDIR:-/tmp}"
stage_parent="${stage_parent%/}"
stage_dir="$(mktemp -d "$stage_parent/mango-cma-docs.XXXXXX")"
readonly stage_parent stage_dir

cleanup() {
  case "$stage_dir" in
    "$stage_parent"/mango-cma-docs.*)
      rm -rf -- "$stage_dir"
      ;;
    *)
      printf 'refusing to remove unexpected staging directory: %s\n' "$stage_dir" >&2
      ;;
  esac
}
trap cleanup EXIT

readonly staged_snapshot="$stage_dir/snapshot"
readonly catalog_file="$stage_dir/llms.txt"
readonly guide_list="$stage_dir/guides.tsv"
readonly api_list="$stage_dir/api-reference.tsv"

mkdir -p "$staged_snapshot/guides" "$staged_snapshot/api-reference"

curl --fail --silent --show-error --location --retry 3 \
  "$CATALOG_URL" --output "$catalog_file"

# The official llms.txt catalog defines the complete English Managed Agents
# guide section. Keep discovery automatic so new upstream pages are picked up.
awk '
  /^### Managed Agents$/ { active=1; next }
  active && /^### / { active=0 }
  active { print }
' "$catalog_file" \
  | sed -nE 's#^- \[([^]]+)\]\((https://platform\.claude\.com/docs/en/managed-agents/[^)]*\.md)\).*$#\1\t\2#p' \
  | sort -u > "$guide_list"

# The guide section is paired with the public Beta API resource families that
# make up the Managed Agents surface. Messages and Models Beta pages are not
# included because they document the separate Messages API.
sed -nE 's#^- \[([^]]+)\]\((https://platform\.claude\.com/docs/en/api/beta/(agents|deployment_runs|deployments|dreams|environments|files|memory_stores|sessions|skills|tunnels|user_profiles|vaults|webhooks)(/[^)]*)?\.md)\).*$#\1\t\2#p' \
  "$catalog_file" | sort -u > "$api_list"

if [[ ! -s "$guide_list" || ! -s "$api_list" ]]; then
  printf 'official catalog did not contain the expected CMA documentation sets\n' >&2
  exit 1
fi

download_page() {
  local url="$1"
  local relative_path="$2"
  local target="$staged_snapshot/$relative_path"
  local temporary_target="$target.download"

  mkdir -p "$(dirname -- "$target")"
  curl --fail --silent --show-error --location --retry 3 \
    "$url" --output "$temporary_target"

  if [[ ! -s "$temporary_target" ]] || ! head -n 1 "$temporary_target" | grep -Eq '^#'; then
    printf 'downloaded page is not non-empty Markdown: %s\n' "$url" >&2
    exit 1
  fi

  mv -- "$temporary_target" "$target"
}

readonly manifest="$staged_snapshot/MANIFEST.tsv"
printf 'category\ttitle\tlocal_path\tupstream_url\tsha256\n' > "$manifest"

while IFS=$'\t' read -r title url; do
  page="${url#"$DOCS_BASE_URL/managed-agents/"}"
  relative_path="guides/$page"
  download_page "$url" "$relative_path"
  checksum="$(shasum -a 256 "$staged_snapshot/$relative_path" | cut -d ' ' -f 1)"
  printf 'guide\t%s\t%s\t%s\t%s\n' \
    "$title" "$relative_path" "$url" "$checksum" >> "$manifest"
done < "$guide_list"

while IFS=$'\t' read -r title url; do
  page="${url#"$DOCS_BASE_URL/api/beta/"}"
  relative_path="api-reference/$page"
  download_page "$url" "$relative_path"
  checksum="$(shasum -a 256 "$staged_snapshot/$relative_path" | cut -d ' ' -f 1)"
  printf 'api-reference\t%s\t%s\t%s\t%s\n' \
    "$title" "$relative_path" "$url" "$checksum" >> "$manifest"
done < "$api_list"

readonly fetched_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
readonly guide_count="$(wc -l < "$guide_list" | tr -d ' ')"
readonly api_count="$(wc -l < "$api_list" | tr -d ' ')"
readonly index="$staged_snapshot/INDEX.md"

{
  printf '# Claude Managed Agents documentation snapshot\n\n'
  printf -- '- Fetched at: `%s`\n' "$fetched_at"
  printf -- '- Catalog: <%s>\n' "$CATALOG_URL"
  printf -- '- Scope: %s Managed Agents guides and %s related Beta API reference pages\n\n' \
    "$guide_count" "$api_count"
  printf 'The hosted official documentation is authoritative if this snapshot differs. '
  printf 'Run `./scripts/sync-cma-docs.sh` to refresh it.\n\n'
  printf '## Guides\n\n'
  while IFS=$'\t' read -r title url; do
    page="${url#"$DOCS_BASE_URL/managed-agents/"}"
    printf -- '- [%s](guides/%s) ([upstream](%s))\n' "$title" "$page" "$url"
  done < "$guide_list"
  printf '\n## Beta API reference\n\n'
  while IFS=$'\t' read -r title url; do
    page="${url#"$DOCS_BASE_URL/api/beta/"}"
    printf -- '- [%s](api-reference/%s) ([upstream](%s))\n' "$title" "$page" "$url"
  done < "$api_list"
} > "$index"

mkdir -p "$DEST_DIR"
rsync -a --delete "$staged_snapshot/" "$DEST_DIR/"

printf 'CMA documentation snapshot updated: %s guides, %s API pages\n' \
  "$guide_count" "$api_count"

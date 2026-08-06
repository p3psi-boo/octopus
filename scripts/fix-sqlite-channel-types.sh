#!/usr/bin/env bash

set -euo pipefail

usage() {
    cat <<'EOF'
Usage:
  scripts/fix-sqlite-channel-types.sh [--dry-run] /path/to/data.db

Converts old numeric channels.type values to axonhub/llm string values:
  0 -> openai/chat_completions
  1 -> openai/responses
  2 -> anthropic/messages
  3 -> gemini/contents
  4 -> doubao
  5 -> openai/embeddings

The script only supports SQLite databases. It creates a timestamped .bak copy
before modifying the database unless --dry-run is used.
EOF
}

dry_run=false
if [[ "${1:-}" == "--dry-run" ]]; then
    dry_run=true
    shift
fi

db_path="${1:-}"
if [[ -z "$db_path" || "${2:-}" != "" ]]; then
    usage
    exit 2
fi

if [[ ! -f "$db_path" ]]; then
    echo "error: database not found: $db_path" >&2
    exit 1
fi

if ! command -v sqlite3 >/dev/null 2>&1; then
    echo "error: sqlite3 is required" >&2
    exit 1
fi

integrity="$(sqlite3 "$db_path" "PRAGMA integrity_check;" 2>&1)"
if [[ "$integrity" != "ok" ]]; then
    echo "error: database integrity check failed:" >&2
    echo "$integrity" >&2
    exit 1
fi

has_channels="$(sqlite3 "$db_path" "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='channels';")"
if [[ "$has_channels" != "1" ]]; then
    echo "error: channels table not found" >&2
    exit 1
fi

echo "Database: $db_path"
echo "Current channels.type values:"
sqlite3 "$db_path" "SELECT type, COUNT(*) FROM channels GROUP BY type ORDER BY type;"

numeric_count="$(sqlite3 "$db_path" "SELECT COUNT(*) FROM channels WHERE CAST(type AS TEXT) IN ('0','1','2','3','4','5');")"
unknown_count="$(sqlite3 "$db_path" "SELECT COUNT(*) FROM channels WHERE CAST(type AS TEXT) NOT IN ('0','1','2','3','4','5','openai/chat_completions','openai/responses','anthropic/messages','gemini/contents','doubao','openai/embeddings');")"

if [[ "$unknown_count" != "0" ]]; then
    echo "warning: found $unknown_count channels.type values outside known old/new mappings:" >&2
    sqlite3 "$db_path" "SELECT id, name, type FROM channels WHERE CAST(type AS TEXT) NOT IN ('0','1','2','3','4','5','openai/chat_completions','openai/responses','anthropic/messages','gemini/contents','doubao','openai/embeddings') ORDER BY id;" >&2
fi

if [[ "$numeric_count" == "0" ]]; then
    echo "No numeric channel types found. Nothing to do."
    exit 0
fi

echo "Numeric channel types to migrate: $numeric_count"

if [[ "$dry_run" == "true" ]]; then
    echo "Dry run only. Planned conversion preview:"
    sqlite3 "$db_path" "
SELECT id,
       name,
       type AS old_type,
       CASE CAST(type AS TEXT)
           WHEN '0' THEN 'openai/chat_completions'
           WHEN '1' THEN 'openai/responses'
           WHEN '2' THEN 'anthropic/messages'
           WHEN '3' THEN 'gemini/contents'
           WHEN '4' THEN 'doubao'
           WHEN '5' THEN 'openai/embeddings'
           ELSE CAST(type AS TEXT)
       END AS new_type
FROM channels
WHERE CAST(type AS TEXT) IN ('0','1','2','3','4','5')
ORDER BY id;
"
    exit 0
fi

backup_path="${db_path}.bak-$(date +%Y%m%d%H%M%S)"
cp "$db_path" "$backup_path"
echo "Backup created: $backup_path"

sqlite3 "$db_path" <<'SQL'
BEGIN IMMEDIATE;
UPDATE channels
SET type = CASE CAST(type AS TEXT)
    WHEN '0' THEN 'openai/chat_completions'
    WHEN '1' THEN 'openai/responses'
    WHEN '2' THEN 'anthropic/messages'
    WHEN '3' THEN 'gemini/contents'
    WHEN '4' THEN 'doubao'
    WHEN '5' THEN 'openai/embeddings'
    ELSE CAST(type AS TEXT)
END
WHERE CAST(type AS TEXT) IN ('0','1','2','3','4','5');
COMMIT;
SQL

echo "Migration complete. New channels.type values:"
sqlite3 "$db_path" "SELECT type, COUNT(*) FROM channels GROUP BY type ORDER BY type;"

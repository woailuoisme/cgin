#!/bin/bash
set -euo pipefail

FILE="${1:-secrets.ini}"

if ! command -v gh >/dev/null 2>&1; then
    echo "Error: gh CLI tool not found. Install from: https://cli.github.com/" >&2
    exit 1
fi

if [ ! -f "$FILE" ]; then
    echo "Error: Secrets file '$FILE' not found." >&2
    exit 1
fi

echo "Syncing GitHub Secrets from $FILE..."

while IFS='=' read -r key value || [ -n "$key" ]; do
    # Skip comments and empty lines
    [[ "$key" =~ ^[[:space:]]*# ]] || [[ -z "$key" ]] && continue

    # Trim leading/trailing whitespace using pure shell parameter expansion
    key="${key#"${key%%[![:space:]]*}"}"
    key="${key%"${key##*[![:space:]]}"}"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"

    [[ -z "$key" ]] && continue

    echo "Setting secret: $key"
    gh secret set "$key" --body "$value"
done < "$FILE"

echo "Sync completed successfully!"


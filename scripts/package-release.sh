#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

APP_NAME="lmy-harness-agent"
VERSION="$(node -p "require('./package.json').version")"
GOOS_VALUE="$(go env GOOS)"
GOARCH_VALUE="$(go env GOARCH)"
PACKAGE_NAME="${APP_NAME}-${VERSION}-${GOOS_VALUE}-${GOARCH_VALUE}"
RELEASE_DIR="$ROOT_DIR/release"
STAGE_DIR="$RELEASE_DIR/$PACKAGE_NAME"
ARCHIVE_PATH="$RELEASE_DIR/$PACKAGE_NAME.tar.gz"

echo "Building $APP_NAME $VERSION for $GOOS_VALUE/$GOARCH_VALUE..."
npm run build

rm -rf "$STAGE_DIR" "$ARCHIVE_PATH"
mkdir -p \
  "$STAGE_DIR/bin" \
  "$STAGE_DIR/apps/web" \
  "$STAGE_DIR/apps/server/data/knowledge/files" \
  "$STAGE_DIR/apps/server/data/knowledge/parsed"

cp "$ROOT_DIR/apps/server/bin/harness-server" "$STAGE_DIR/bin/harness-server"
chmod +x "$STAGE_DIR/bin/harness-server"
cp -R "$ROOT_DIR/apps/web/dist" "$STAGE_DIR/apps/web/dist"

if [ -d "$ROOT_DIR/skills" ]; then
  cp -R "$ROOT_DIR/skills" "$STAGE_DIR/skills"
fi

if [ -f "$ROOT_DIR/README.md" ]; then
  cp "$ROOT_DIR/README.md" "$STAGE_DIR/README.md"
fi

cat > "$STAGE_DIR/.env.example" <<'EOF'
# Copy this file to .env and fill values as needed.
# The app can also start without this file; model configs can be edited in the UI.

ADDR=127.0.0.1:3000

OPENAI_API_KEY=
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_MODEL=gpt-4o-mini

# Optional: required only when using knowledge-base vector retrieval.
OPENAI_EMBEDDING_API_KEY=
OPENAI_EMBEDDING_BASE_URL=https://api.openai.com/v1
OPENAI_EMBEDDING_MODEL=text-embedding-3-small
EOF

cat > "$STAGE_DIR/start.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

warn() {
  printf 'Warning: %s\n' "$*" >&2
}

fail() {
  printf 'Error: %s\n' "$*" >&2
  exit 1
}

install_hint() {
  cat >&2 <<'HINT'

PDF knowledge-base import requires Poppler's pdftotext.
Install it only if you need to import PDF files:

  macOS:
    brew install poppler

  Debian/Ubuntu:
    sudo apt-get update
    sudo apt-get install -y poppler-utils

HINT
}

[ -x "$ROOT_DIR/bin/harness-server" ] || fail "missing executable: $ROOT_DIR/bin/harness-server"
[ -f "$ROOT_DIR/apps/web/dist/index.html" ] || fail "missing frontend file: $ROOT_DIR/apps/web/dist/index.html"
[ -f "$ROOT_DIR/apps/web/dist/main.js" ] || fail "missing frontend file: $ROOT_DIR/apps/web/dist/main.js"

if ! command -v pdftotext >/dev/null 2>&1; then
  warn "pdftotext was not found; PDF import will be unavailable."
  install_hint
fi

mkdir -p apps/server/data/knowledge/files apps/server/data/knowledge/parsed

if [ -f "$ROOT_DIR/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  . "$ROOT_DIR/.env"
  set +a
fi

export PROJECT_ROOT="${PROJECT_ROOT:-$ROOT_DIR}"
export WEB_DIST_DIR="${WEB_DIST_DIR:-$ROOT_DIR/apps/web/dist}"
export ADDR="${ADDR:-127.0.0.1:3000}"

echo "Starting Lmy' Harness Agent..."
echo "URL: http://$ADDR/"
echo "Data: $ROOT_DIR/apps/server/data"
echo

exec "$ROOT_DIR/bin/harness-server"
EOF
chmod +x "$STAGE_DIR/start.sh"

cat > "$STAGE_DIR/start.command" <<'EOF'
#!/usr/bin/env bash
DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$DIR"
exec "$DIR/start.sh"
EOF
chmod +x "$STAGE_DIR/start.command"

cat > "$STAGE_DIR/README-RELEASE.md" <<EOF
# Lmy' Harness Agent Release

This package is built for \`$GOOS_VALUE/$GOARCH_VALUE\`.

## Start

\`\`\`bash
./start.sh
\`\`\`

On macOS, you can also double-click \`start.command\`.

The app listens on:

\`\`\`text
http://127.0.0.1:3000/
\`\`\`

To use a different address:

\`\`\`bash
ADDR=127.0.0.1:3001 ./start.sh
\`\`\`

## Model Configuration

You can configure models in the UI, or copy \`.env.example\` to \`.env\` and fill in API values:

\`\`\`bash
cp .env.example .env
./start.sh
\`\`\`

## Runtime Data

Runtime data is created inside:

\`\`\`text
apps/server/data
\`\`\`

This directory stores SQLite state, conversations, model configs, knowledge-base files, chunks, FTS5 indexes and sqlite-vec vectors. Do not publish it if it contains private data.
EOF

find "$STAGE_DIR" -name ".DS_Store" -delete

(
  cd "$RELEASE_DIR"
  tar -czf "$ARCHIVE_PATH" "$PACKAGE_NAME"
)

echo "Release package created:"
echo "$ARCHIVE_PATH"

#!/usr/bin/env sh
set -eu

TEMPLATE_PATH="/etc/nginx/templates/default.conf.template"
OUT_PATH="/etc/nginx/conf.d/default.conf"

# Defaults (also declared in Dockerfile, but keep here for robustness)
: "${BACKEND_SERVICE_HOST:=kubemorph-server}"
: "${BACKEND_SERVICE_PORT:=8080}"
: "${BACKEND_API_PREFIX:=/api}"
: "${BACKEND_API_VERSION_PREFIX:=/api/v1}"

# Render template
if [ ! -f "$TEMPLATE_PATH" ]; then
  echo "ERROR: nginx template not found at $TEMPLATE_PATH" >&2
  exit 1
fi

envsubst '${BACKEND_SERVICE_HOST} ${BACKEND_SERVICE_PORT} ${BACKEND_API_PREFIX} ${BACKEND_API_VERSION_PREFIX}' \
  < "$TEMPLATE_PATH" \
  > "$OUT_PATH"

echo "Rendered nginx config:" >&2
echo "  BACKEND_SERVICE_HOST=$BACKEND_SERVICE_HOST" >&2
echo "  BACKEND_SERVICE_PORT=$BACKEND_SERVICE_PORT" >&2
echo "  BACKEND_API_PREFIX=$BACKEND_API_PREFIX" >&2
echo "  BACKEND_API_VERSION_PREFIX=$BACKEND_API_VERSION_PREFIX" >&2

exec "$@"

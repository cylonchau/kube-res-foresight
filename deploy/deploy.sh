#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DOCKER_DIR="${SCRIPT_DIR}/docker"
DOCKER_CLI="${DOCKER:-docker}"
ENVIRONMENT="${2:-dev}"
ACTION="${1:-help}"

if [[ "${ENVIRONMENT}" == "prod" ]]; then
  ENV_FILE="${DOCKER_DIR}/.env.prod"
  ENV_EXAMPLE="${DOCKER_DIR}/.env.prod.example"
  FILES=(--file "${DOCKER_DIR}/compose.core.yml" --file "${DOCKER_DIR}/compose.core.prod.yml")
else
  ENV_FILE="${DOCKER_DIR}/.env"
  ENV_EXAMPLE="${DOCKER_DIR}/.env.core.example"
  FILES=(--file "${DOCKER_DIR}/compose.core.yml")
fi

case "${ACTION}" in
  init) [[ -f "${ENV_FILE}" ]] || cp "${ENV_EXAMPLE}" "${ENV_FILE}" ;;
  up) ${DOCKER_CLI} compose --env-file "${ENV_FILE}" "${FILES[@]}" up -d --build --remove-orphans ;;
  start|stop|restart|pull) ${DOCKER_CLI} compose --env-file "${ENV_FILE}" "${FILES[@]}" "${ACTION}" ;;
  down) ${DOCKER_CLI} compose --env-file "${ENV_FILE}" "${FILES[@]}" down --remove-orphans ;;
  status) ${DOCKER_CLI} compose --env-file "${ENV_FILE}" "${FILES[@]}" ps ;;
  logs) ${DOCKER_CLI} compose --env-file "${ENV_FILE}" "${FILES[@]}" logs --tail 200 ;;
  config) ${DOCKER_CLI} compose --env-file "${ENV_FILE}" "${FILES[@]}" config ;;
  *) echo "Usage: bash deploy/deploy.sh <init|up|start|stop|restart|down|status|logs|config|pull> [dev|prod]" ;;
esac

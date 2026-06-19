#!/usr/bin/env bash
# Собирает Ios.xcframework из пакета ios/ для iOS и iOS Simulator.
# Использование: ./scripts/build-ios.sh [путь/к/выходному/каталогу]
# По умолчанию кладёт фреймворк в ./build/ внутри репозитория.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="${1:-"${REPO_ROOT}/build"}"
FRAMEWORK="${OUT_DIR}/Ios.xcframework"

if ! command -v gomobile &>/dev/null; then
  echo "gomobile не найден. Установите:"
  echo "  go install golang.org/x/mobile/cmd/gomobile@latest"
  echo "  gomobile init"
  exit 1
fi

mkdir -p "${OUT_DIR}"
rm -rf "${FRAMEWORK}"

echo "Собираем Ios.xcframework → ${FRAMEWORK}"
cd "${REPO_ROOT}"
gomobile bind \
  -target ios,iossimulator \
  -o "${FRAMEWORK}" \
  github.com/samosvalishe/free-turn-proxy/ios

echo "Готово: ${FRAMEWORK}"

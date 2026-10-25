#!/bin/bash

PROJECT_ROOT=$(cd $(dirname ${BASH_SOURCE[0]})/..; pwd)
set -e

OUT_DIR="target/bin"
GO_CMD=$(command -v go || echo "")

if [ -z "$GO_CMD" ]; then
    echo "Error: 'go' command not found."
    exit 1
fi

RAW_NAME=$1

# 确认传入的模块名
if [ -z "$RAW_NAME" ]; then
    echo "Error: No module name specified."
    echo "Usage: $0 <module_name>"
    exit 1
fi

# 剥离可能存在的 kubeforecast- 前缀用于查找 cmd 目录中的源码
CLEAN_NAME="${RAW_NAME#kubeforecast-}"

# 确定编译路径和模块标识
if [ -d "${PROJECT_ROOT}/cmd/${RAW_NAME}" ]; then
    SRC_PATH="${PROJECT_ROOT}/cmd/${RAW_NAME}"
    MODULE_KEY="${RAW_NAME}"
elif [ -d "${PROJECT_ROOT}/cmd/${CLEAN_NAME}" ]; then
    SRC_PATH="${PROJECT_ROOT}/cmd/${CLEAN_NAME}"
    MODULE_KEY="${CLEAN_NAME}"
elif [ -f "${PROJECT_ROOT}/cmd/${RAW_NAME}.go" ]; then
    SRC_PATH="${PROJECT_ROOT}/cmd/${RAW_NAME}.go"
    MODULE_KEY="${RAW_NAME}"
elif [ -f "${PROJECT_ROOT}/cmd/${CLEAN_NAME}.go" ]; then
    SRC_PATH="${PROJECT_ROOT}/cmd/${CLEAN_NAME}.go"
    MODULE_KEY="${CLEAN_NAME}"
elif [ "$RAW_NAME" == "kubeforecast" ] || [ "$RAW_NAME" == "main.go" ] || [ "$RAW_NAME" == "main" ]; then
    MODULE_KEY="kubeforecast"
    if [ -d "${PROJECT_ROOT}/cmd" ]; then
        SRC_PATH="${PROJECT_ROOT}/cmd"
    elif [ -f "${PROJECT_ROOT}/cmd/main.go" ]; then
        SRC_PATH="${PROJECT_ROOT}/cmd/main.go"
    elif [ -f "${PROJECT_ROOT}/main.go" ]; then
        SRC_PATH="${PROJECT_ROOT}/main.go"
    fi
fi

if [ -z "$SRC_PATH" ]; then
    echo "Error: Could not find source for module '${RAW_NAME}'."
    echo "Checked: cmd/${RAW_NAME}, cmd/${CLEAN_NAME}, cmd/${RAW_NAME}.go, cmd/${CLEAN_NAME}.go, cmd/main.go, main.go"
    exit 119
fi

[ -d "${PROJECT_ROOT}/${OUT_DIR}" ] || mkdir -p "${PROJECT_ROOT}/${OUT_DIR}"

# 确定最终编译输出的二进制名称（kubeforecast 或 kubeforecast-operator）
if [ "$MODULE_KEY" == "kubeforecast" ]; then
    TARGET_BINARY_NAME="kubeforecast"
elif [[ "$MODULE_KEY" == kubeforecast-* ]]; then
    TARGET_BINARY_NAME="${MODULE_KEY}"
else
    TARGET_BINARY_NAME="kubeforecast-${MODULE_KEY}"
fi

# 获取版本号
VERSION=$(git describe --tags --always 2>/dev/null || echo "v0.0.0-dev")

# 编译 (使用本地平台默认设置，方便本地测试；发布时可通过环境变量指定)
TARGET_OS=${GOOS:-$(go env GOHOSTOS)}
TARGET_ARCH=${GOARCH:-$(go env GOHOSTARCH)}
HOST_OS=$(go env GOHOSTOS)
HOST_ARCH=$(go env GOHOSTARCH)

# 根据平台决定扩展名
EXTENSION=""
if [ "$TARGET_OS" == "windows" ]; then
    EXTENSION=".exe"
fi

BINARY_PATH="${PROJECT_ROOT}/${OUT_DIR}/${TARGET_BINARY_NAME}"
# 如果是跨平台编译，在文件名中加入 OS 和 ARCH
if [ "$TARGET_OS" != "$HOST_OS" ] || [ "$TARGET_ARCH" != "$HOST_ARCH" ]; then
    BINARY_PATH="${BINARY_PATH}-${TARGET_OS}-${TARGET_ARCH}${EXTENSION}"
else
    BINARY_PATH="${BINARY_PATH}${EXTENSION}"
fi

# 复制前端编译产物 (Embed)
if [ "${MODULE_KEY}" == "kubeforecast" ]; then
    echo "Packaging frontend assets for kubeforecast..."
    if [ ! -d "${PROJECT_ROOT}/web/dist" ] && [ -f "${PROJECT_ROOT}/web/package.json" ]; then
        if ! command -v pnpm >/dev/null 2>&1; then
            echo "Error: 'pnpm' is required to build the embedded frontend."
            exit 1
        fi
        (cd "${PROJECT_ROOT}/web" && pnpm build)
    fi
    if [ -d "${PROJECT_ROOT}/web/dist" ]; then
        mkdir -p "${PROJECT_ROOT}/pkg/app/router/dist"
        rm -rf "${PROJECT_ROOT}/pkg/app/router/dist/"*
        cp -rf "${PROJECT_ROOT}/web/dist/"* "${PROJECT_ROOT}/pkg/app/router/dist/"
    else
        echo "Warning: web/dist not found. Frontend might be missing from embed."
    fi
fi

# 编译
cd "${PROJECT_ROOT}" && \
    CGO_ENABLED=0 GOOS=${TARGET_OS} GOARCH=${TARGET_ARCH} "${GO_CMD}" build \
    -ldflags "-s -w -X 'github.com/cylonchau/kubeforecast/pkg/version.Version=${VERSION}'" \
    -o "${BINARY_PATH}" "${SRC_PATH}"

if command -v upx >/dev/null 2>&1; then
    if [[ "$TARGET_OS" == "darwin" ]]; then
       echo "Skipping UPX compression for macOS to ensure stability."
    else
       echo "Compressing ${BINARY_PATH} with UPX..."
       upx -1 "${BINARY_PATH}" 2>/dev/null || upx "${BINARY_PATH}" 2>/dev/null || true
    fi
fi

echo "Done building ${TARGET_BINARY_NAME} for ${TARGET_OS}/${TARGET_ARCH}."

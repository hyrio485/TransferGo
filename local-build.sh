#!/usr/bin/env sh

set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
output_dir="$script_dir/dist"

cd "$script_dir"
mkdir -p "$output_dir"

# amd64 表示 Intel 和 AMD 的 64 位处理器架构。
# arm64 表示 Apple Silicon、Windows ARM 和 Linux ARM64 使用的处理器架构。
# darwin 是 Go 对 macOS 操作系统使用的名称。
for target in \
  linux/amd64 \
  linux/arm64 \
  darwin/amd64 \
  darwin/arm64 \
  windows/amd64 \
  windows/arm64
do
  os="${target%/*}"
  arch="${target#*/}"
  suffix=""

  if [ "$os" = "windows" ]
  then
    suffix=".exe"
  fi

  echo "正在构建：$os/$arch"

  # CGO_ENABLED=0 避免依赖目标平台的 C 编译器，并生成独立二进制文件。
  # -trimpath 去除本机构建路径，-ldflags="-s -w" 移除调试信息以减小产物体积。
  CGO_ENABLED=0 \
  GOOS="$os" \
  GOARCH="$arch" \
  go build \
    -trimpath \
    -ldflags="-s -w" \
    -o "$output_dir/transfergo-$os-$arch$suffix" \
    .
done

echo "构建完成，产物目录：$output_dir"

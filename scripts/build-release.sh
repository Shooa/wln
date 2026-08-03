#!/usr/bin/env bash
set -euo pipefail

release_tag="${1:-dev}"
version="${release_tag#v}"
repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dist_dir="${repository_root}/dist"
stage_dir="${dist_dir}/.stage"

rm -rf "${dist_dir}"
mkdir -p "${stage_dir}"

targets=(
  "linux amd64"
  "linux arm64"
  "darwin amd64"
  "darwin arm64"
  "windows amd64"
  "windows arm64"
)

ldflags="-s -w -X github.com/Shooa/wln/internal/app.Version=${version} -X github.com/Shooa/wln/internal/wialon.Version=${version}"

for target in "${targets[@]}"; do
  read -r target_os target_arch <<<"${target}"
  archive_base="wln_${version}_${target_os}_${target_arch}"
  target_dir="${stage_dir}/${archive_base}"
  mkdir -p "${target_dir}"

  binary_name="wln"
  if [[ "${target_os}" == "windows" ]]; then
    binary_name="wln.exe"
  fi

  echo "Building ${target_os}/${target_arch}"
  CGO_ENABLED=0 GOOS="${target_os}" GOARCH="${target_arch}" \
    go build -trimpath -ldflags "${ldflags}" \
    -o "${target_dir}/${binary_name}" "${repository_root}/cmd/wln"

  if [[ "${target_os}" == "windows" ]]; then
    (cd "${target_dir}" && zip -q "${dist_dir}/${archive_base}.zip" "${binary_name}")
  else
    tar -C "${target_dir}" -czf "${dist_dir}/${archive_base}.tar.gz" "${binary_name}"
  fi
done

rm -rf "${stage_dir}"
(
  cd "${dist_dir}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum ./*.tar.gz ./*.zip > SHA256SUMS
  else
    shasum -a 256 ./*.tar.gz ./*.zip > SHA256SUMS
  fi
)

echo "Release artifacts are in ${dist_dir}"

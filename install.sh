#!/bin/sh
set -eu

repository="Shooa/wln"
install_dir="${WLN_INSTALL_DIR:-${HOME}/.local/bin}"
latest_url="https://github.com/${repository}/releases/latest"

if ! command -v curl >/dev/null 2>&1; then
  echo "wln installer: curl is required" >&2
  exit 1
fi

case "$(uname -s)" in
  Darwin) target_os="darwin" ;;
  Linux) target_os="linux" ;;
  *)
    echo "wln installer: unsupported operating system; use install.ps1 on Windows" >&2
    exit 1
    ;;
esac

case "$(uname -m)" in
  x86_64|amd64) target_arch="amd64" ;;
  arm64|aarch64) target_arch="arm64" ;;
  *)
    echo "wln installer: unsupported architecture $(uname -m)" >&2
    exit 1
    ;;
esac

release_url="$(curl -fsSL -o /dev/null -w '%{url_effective}' "${latest_url}")"
release_tag="${release_url##*/}"
version="${release_tag#v}"
if [ -z "${version}" ] || [ "${version}" = "${release_tag}" ]; then
  echo "wln installer: could not determine the latest release" >&2
  exit 1
fi

archive="wln_${version}_${target_os}_${target_arch}.tar.gz"
download_base="https://github.com/${repository}/releases/download/${release_tag}"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/wln-install.XXXXXX")"
trap 'rm -rf "${temporary_dir}"' EXIT HUP INT TERM

echo "Downloading wln ${version} for ${target_os}/${target_arch}..."
curl -fsSL "${download_base}/${archive}" -o "${temporary_dir}/${archive}"
curl -fsSL "${download_base}/SHA256SUMS" -o "${temporary_dir}/SHA256SUMS"

expected="$(awk -v name="${archive}" '$2 == "./" name || $2 == name { print $1; exit }' "${temporary_dir}/SHA256SUMS")"
if [ -z "${expected}" ]; then
  echo "wln installer: release checksum is missing" >&2
  exit 1
fi
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "${temporary_dir}/${archive}" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "${temporary_dir}/${archive}" | awk '{print $1}')"
fi
if [ "${actual}" != "${expected}" ]; then
  echo "wln installer: SHA-256 checksum mismatch" >&2
  exit 1
fi

tar -xzf "${temporary_dir}/${archive}" -C "${temporary_dir}" wln
mkdir -p "${install_dir}"
install -m 0755 "${temporary_dir}/wln" "${install_dir}/wln"

echo "Installed wln ${version} to ${install_dir}/wln"
case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *)
    echo "Add ${install_dir} to PATH, for example:"
    echo "  export PATH=\"${install_dir}:\$PATH\""
    ;;
esac

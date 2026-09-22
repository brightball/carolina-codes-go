#!/usr/bin/env bash
# Prepare/restore the Gitea CI workspace without Node or an OCI push.
#
# The golang:1.25-bookworm job image has no Node, so actions/upload-artifact
# cannot run inside it. Gitea job tokens also cannot publish container
# packages. This helper talks to Gitea's artifact API with curl instead.
set -euo pipefail

ARTIFACT_NAME="${CI_ENV_ARTIFACT_NAME:-prepared-env}"
TAR_PATH="${CI_ENV_TAR:-/tmp/prepared-env.tar.gz}"

# Pinned versions — keep in sync with Makefile / mise.toml.

json_string() {
  key="$1"
  file="$2"
  tr -d '\n' <"$file" | sed -n "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"\\([^\"]*\\)\".*/\\1/p" | head -1
}

artifact_token() {
  t="${ACTIONS_RUNTIME_TOKEN:-${GITHUB_TOKEN:-${GITEA_TOKEN:-}}}"
  if [ -z "$t" ]; then
    echo "missing artifact token (ACTIONS_RUNTIME_TOKEN/GITHUB_TOKEN)" >&2
    exit 1
  fi
  printf '%s' "$t"
}

artifact_base() {
  if [ -n "${ACTIONS_RUNTIME_URL:-}" ]; then
    printf '%s' "${ACTIONS_RUNTIME_URL%/}"
    return
  fi
  if [ -z "${GITHUB_SERVER_URL:-}" ]; then
    echo "missing ACTIONS_RUNTIME_URL or GITHUB_SERVER_URL" >&2
    exit 1
  fi
  printf '%s' "${GITHUB_SERVER_URL%/}/api/actions_pipeline"
}

rewrite_runtime_url() {
  url="$1"
  case "$url" in
    *_apis/*)
      suffix="${url#*_apis/}"
      printf '%s/_apis/%s' "$(artifact_base)" "$suffix"
      ;;
    /*)
      printf '%s%s' "$(artifact_base)" "${url#/api/actions_pipeline}"
      ;;
    *)
      printf '%s' "$url"
      ;;
  esac
}

md5_b64() {
  openssl dgst -md5 -binary "$1" | openssl base64 -A
}

workspace_dir() {
  printf '%s' "${GITHUB_WORKSPACE:-$(pwd)}"
}

cmd_install() {
  echo "fetching Go modules and installing quality-gate tools"
  go mod download
  go install github.com/securego/gosec/v2/cmd/gosec@v2.29.0
  # These two modules declare go 1.26. The job image is Go 1.25 with
  # GOTOOLCHAIN=local, so allow a toolchain download for the build only.
  GOTOOLCHAIN=auto go install golang.org/x/vuln/cmd/govulncheck@v1.8.0
  go install github.com/zricethezav/gitleaks/v8@v8.30.1
  GOTOOLCHAIN=auto go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
}

cmd_pack() {
  ws="$(workspace_dir)"
  gopath="$(go env GOPATH)"
  gomodcache="$(go env GOMODCACHE)"
  (
    stage="$(mktemp -d)"
    trap 'rm -rf "$stage"' EXIT
    mkdir -p "$stage/workspace" "$stage/gopath-bin" "$stage/gomodcache"

    tar -C "$ws" -cf - . | tar -C "$stage/workspace" -xf -

    if [ ! -d "$gopath/bin" ]; then
      echo "GOPATH/bin missing; install tools before packing" >&2
      exit 1
    fi
    tar -C "$gopath/bin" -cf - . | tar -C "$stage/gopath-bin" -xf -

    if [ -d "$gomodcache" ]; then
      tar -C "$gomodcache" -cf - . | tar -C "$stage/gomodcache" -xf -
    fi

    mkdir -p "$(dirname "$TAR_PATH")"
    tar -C "$stage" -czf "$TAR_PATH" workspace gopath-bin gomodcache
  )
  echo "packed $TAR_PATH"
}

cmd_unpack() {
  ws="$(workspace_dir)"
  gopath="$(go env GOPATH)"
  gomodcache="$(go env GOMODCACHE)"
  if [ ! -f "$TAR_PATH" ]; then
    echo "missing tarball $TAR_PATH" >&2
    exit 1
  fi
  (
    stage="$(mktemp -d)"
    trap 'rm -rf "$stage"' EXIT
    tar -C "$stage" -xzf "$TAR_PATH"
    mkdir -p "$ws" "$gopath/bin" "$gomodcache"
    tar -C "$stage/workspace" -cf - . | tar -C "$ws" -xf -
    tar -C "$stage/gopath-bin" -cf - . | tar -C "$gopath/bin" -xf -
    if [ -d "$stage/gomodcache" ]; then
      tar -C "$stage/gomodcache" -cf - . | tar -C "$gomodcache" -xf -
    fi
  )
  echo "restored workspace=$ws GOPATH/bin=$gopath/bin GOMODCACHE=$gomodcache"
}

cmd_upload() {
  if [ ! -f "$TAR_PATH" ]; then
    echo "missing tarball $TAR_PATH" >&2
    exit 1
  fi
  if [ -z "${GITHUB_RUN_ID:-}" ]; then
    echo "missing GITHUB_RUN_ID" >&2
    exit 1
  fi
  token="$(artifact_token)"
  base="$(artifact_base)"
  create_url="${base}/_apis/pipelines/workflows/${GITHUB_RUN_ID}/artifacts?api-version=6.0-preview"
  resp="$(mktemp)"
  curl -fsS -H "Authorization: Bearer ${token}" -H "Content-Type: application/json" \
    -X POST --data "{\"Type\":\"actions_storage\",\"Name\":\"${ARTIFACT_NAME}\"}" \
    "$create_url" -o "$resp"
  upload_url="$(json_string fileContainerResourceUrl "$resp")"
  rm -f "$resp"
  if [ -z "$upload_url" ]; then
    echo "artifact create did not return fileContainerResourceUrl" >&2
    exit 1
  fi
  upload_url="$(rewrite_runtime_url "$upload_url")"
  size="$(wc -c <"$TAR_PATH" | tr -d ' ')"
  md5="$(md5_b64 "$TAR_PATH")"
  filename="$(basename "$TAR_PATH")"
  put_url="${upload_url}?itemPath=${ARTIFACT_NAME}%2F${filename}"
  curl -fsS -H "Authorization: Bearer ${token}" \
    -H "x-actions-results-md5: ${md5}" \
    -H "x-tfs-filelength: ${size}" \
    -H "content-range: bytes 0-$((size - 1))/${size}" \
    -X PUT --data-binary "@${TAR_PATH}" \
    "$put_url" -o /dev/null
  curl -fsS -H "Authorization: Bearer ${token}" \
    -X PATCH \
    "${base}/_apis/pipelines/workflows/${GITHUB_RUN_ID}/artifacts?api-version=6.0-preview&artifactName=${ARTIFACT_NAME}" \
    -o /dev/null
  echo "uploaded artifact ${ARTIFACT_NAME}"
}

cmd_download() {
  if [ -z "${GITHUB_RUN_ID:-}" ]; then
    echo "missing GITHUB_RUN_ID" >&2
    exit 1
  fi
  token="$(artifact_token)"
  base="$(artifact_base)"
  list_url="${base}/_apis/pipelines/workflows/${GITHUB_RUN_ID}/artifacts?api-version=6.0-preview"
  resp="$(mktemp)"
  curl -fsS -H "Authorization: Bearer ${token}" "$list_url" -o "$resp"
  container_url="$(json_string fileContainerResourceUrl "$resp")"
  rm -f "$resp"
  if [ -z "$container_url" ]; then
    echo "artifact list did not return fileContainerResourceUrl" >&2
    exit 1
  fi
  container_url="$(rewrite_runtime_url "$container_url")"
  files="$(mktemp)"
  curl -fsS -H "Authorization: Bearer ${token}" \
    "${container_url}?itemPath=${ARTIFACT_NAME}" -o "$files"
  content_url="$(json_string contentLocation "$files")"
  item_path="$(json_string path "$files")"
  rm -f "$files"
  if [ -z "$content_url" ]; then
    echo "artifact download_url did not return contentLocation" >&2
    exit 1
  fi
  content_url="$(rewrite_runtime_url "$content_url")"
  mkdir -p "$(dirname "$TAR_PATH")"
  encoded_path="$(printf '%s' "$item_path" | sed 's|/|%2F|g')"
  curl -fsS -H "Authorization: Bearer ${token}" \
    "${content_url}?itemPath=${encoded_path}" -o "$TAR_PATH"
  echo "downloaded $TAR_PATH"
}

cmd_prepare() {
  if [ "${CI_ENV_SKIP_INSTALL:-}" != "1" ]; then
    cmd_install
  fi
  cmd_pack
  cmd_upload
}

cmd_restore() {
  echo "restoring prepared environment"
  cmd_download
  cmd_unpack
  # GOPATH/bin is on PATH in golang images; keep it first for restored tools.
  export PATH="$(go env GOPATH)/bin:${PATH}"
}

usage() {
  echo "usage: $0 prepare|restore|install|pack|unpack|upload|download" >&2
  exit 2
}

cmd="${1:-}"
case "$cmd" in
  prepare) cmd_prepare ;;
  restore) cmd_restore ;;
  install) cmd_install ;;
  pack) cmd_pack ;;
  unpack) cmd_unpack ;;
  upload) cmd_upload ;;
  download) cmd_download ;;
  *) usage ;;
esac

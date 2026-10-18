#!/usr/bin/env bash
# Regenerate the generated/ package from openapi/v1.json.
#
# openapi/v1.json is a copy of the GuardianO3 API contract. It is updated by a
# pull request opened from the API repository whenever the contract changes;
# this script turns it into code. generated/ is never edited by hand: CI
# regenerates it and fails if the result differs from what is committed. The
# hand-written packages, client/ and live/, are never touched.
#
# Runs the generator in Docker, so no JVM is needed.
#
# Usage: ./scripts/generate.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="openapitools/openapi-generator-cli:v7.15.0"

MOUNT="$ROOT"
if command -v cygpath >/dev/null 2>&1; then
  # Git Bash on Windows: Docker wants a Windows path, and MSYS must not rewrite it.
  MOUNT="$(cygpath -m "$ROOT")"
  export MSYS_NO_PATHCONV=1
fi

rm -rf "${ROOT}/generated"
mkdir -p "${ROOT}/generated"

# Keep the package source; drop the generator's own go.mod/go.sum (this
# repository is one module, defined by the go.mod at its root), its README
# (whose links point at docs that are not kept), the YAML copy of the
# contract, its test stubs and its CI files.
cat > "${ROOT}/generated/.openapi-generator-ignore" <<'IGNORE'
docs/**
test/**
api/**
.github/**
.travis.yml
.gitlab-ci.yml
git_push.sh
.gitignore
README.md
go.mod
go.sum
IGNORE

# gitUserId/gitRepoId only feed the generator's go.mod and README, both
# dropped above; set them anyway so nothing it emits names a placeholder.
docker run --rm \
  -v "${MOUNT}:/work" \
  -u "$(id -u):$(id -g)" \
  "$IMAGE" generate \
    -i /work/openapi/v1.json \
    -g go \
    -o /work/generated \
    --skip-validate-spec \
    --git-user-id guardiano3 \
    --git-repo-id guardiano3-go/generated \
    --additional-properties="packageName=guardiano3,isGoSubmodule=false" \
    >/dev/null

# The generator does not run gofmt; Go code that is not gofmt-clean reads as
# unfinished, and every Go tool expects it. Same toolchain image CI uses.
docker run --rm \
  -v "${MOUNT}:/work" \
  -u "$(id -u):$(id -g)" \
  golang:1.23 gofmt -w /work/generated

echo "Regenerated generated/."

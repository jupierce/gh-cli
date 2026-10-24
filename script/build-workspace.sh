#!/usr/bin/env bash
# Build the fork reproducibly, including an inventory of the exact local source.
set -euo pipefail
cd "$(dirname "$0")/.."

task_output=${1:-bin/gh}
export GOTOOLCHAIN
GOTOOLCHAIN=$(awk '$1 == "toolchain" { print $2 }' go.mod)
export CGO_ENABLED=0
export SOURCE_DATE_EPOCH=${SOURCE_DATE_EPOCH:-$(git show -s --format=%ct HEAD)}
task_date=$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%d)
task_source=$(git ls-files -cz --cached --others --exclude-standard | LC_ALL=C sort -zu | xargs -0 sha256sum | sha256sum | cut -d ' ' -f 1)
task_version=2.103.2+chai.3
task_dirty=no
if [[ -n $(git status --porcelain --untracked-files=normal) ]]; then
  task_dirty=yes
  task_version+=.dev.${task_source:0:12}
fi
mkdir -p "$(dirname "$task_output")"
go build -trimpath -buildvcs=false -mod=readonly \
  -ldflags "-X github.com/cli/cli/v2/internal/build.Version=$task_version -X github.com/cli/cli/v2/internal/build.Date=$task_date" \
  -o "$task_output" ./cmd/gh

{
  printf 'version=%s\n' "$task_version"
  printf 'upstream_commit=%s\n' 4f2cfe92e3499f8581bf39db77e0a1431fa7b55c
  printf 'source_commit=%s\n' "$(git rev-parse HEAD)"
  printf 'source_sha256=%s\n' "$task_source"
  printf 'dirty=%s\n' "$task_dirty"
  printf 'protocol=%s\n' chai.github-proxy/v1
  printf 'toolchain=%s\n' "$(go version)"
  printf 'target=%s/%s\n' "$(go env GOOS)" "$(go env GOARCH)"
  printf 'source_date_epoch=%s\n' "$SOURCE_DATE_EPOCH"
  printf 'go_sum_sha256=%s\n' "$(sha256sum go.sum | cut -d ' ' -f 1)"
  printf 'binary_sha256=%s\n' "$(sha256sum "$task_output" | cut -d ' ' -f 1)"
} > "$task_output.provenance"
printf 'Built %s (%s)\n' "$task_output" "$task_version"

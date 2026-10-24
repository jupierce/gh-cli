# Chai workspace integration

This fork adds an opt-in client for a credential-holding workspace manager.
Base: upstream **v2.103.2**, commit `4f2cfe92e3499f8581bf39db77e0a1431fa7b55c`.
Patch revision: **chai.2**. Wire protocol: **chai.github-proxy/v1**.
The manager gateway is a separate implementation; these patches alone do not
enable the feature in a deployed workspace.

## Configure

Set `GH_WORKSPACE_CONFIG=/run/chai-gh/workspace.json`. The manager supplies this
public descriptor and the socket, mounted read-only to the worker where possible:

```json
{
  "protocol": "chai.github-proxy/v1",
  "socket": "/run/chai-gh/api.sock",
  "host": "github.com",
  "git_proxy_prefixes": ["http://localhost:8080/github.com/"]
}
```

No token, `gh auth login`, `hosts.yml`, or `api_host` alias is required. Use an
isolated `GH_CONFIG_DIR` without API host aliases. Ordinary preferences such as
pager and editor can coexist there. Provision HTTPS Git transport and the existing
Git `insteadOf` rules and signing wrapper independently. Set `GH_NO_UPDATE_NOTIFIER=1`,
`GH_NO_EXTENSION_UPDATE_NOTIFIER=1`, and `GH_TELEMETRY=false` in the worker image.

An unset `GH_WORKSPACE_CONFIG` preserves upstream behavior. An empty, missing,
invalid, or incompatible descriptor fails; it does not select ordinary auth.
The selected socket uses upstream go-gh's Unix transport, including for absolute
URLs and pagination. Standard auth headers and cookies are stripped before HTTP
logging. Explicit `gh api --cache` is unsupported; live policy-aware caching belongs
to the manager. Only canonical `github.com` and `api.github.com` destinations are
supported in this revision. Other download hosts require later qualification.

Git discovery translates only declared scheme/authority/path prefixes. It composes
with upstream SSH alias translation and preserves the original `git.Remote` URLs.
Clone, fetch, checkout, hooks, signing, and push still use the configured Git binary.
The fork does not provide a push authorization path.

## Command contract

Native parsing, API queries, output formatting, JSON selection, jq, templates,
pagination, and exit behavior are retained for supported command forms. The manager
must implement the read profiles those commands need; the client manifest does not
claim that every native API query is already deployed server-side.
Default PR and issue previews omit project metadata, whose access scope is
separate from repository content. Explicit JSON field selections are preserved
and validated by the gateway.
Default PR previews use the team's `combinedSlug` to preserve its full `org/team`
identity without the `read:org` scope required by a separate organization lookup.

| Commands | Workspace behavior |
| --- | --- |
| `api` | Native API requests over the socket; manager validates methods, paths and GraphQL operations. |
| `pr view/list/status/diff/checks/checkout` | Native reads and existing Git checkout path. |
| `issue view/list/status`, `repo view/clone/set-default`, `search repos/issues/prs/code/commits` | Native reads and existing local Git behavior. |
| `auth status` | Manager identity; `--json hosts` works; no token is printed, including with `--show-token`. |
| `auth token` and other auth commands | Clear unsupported error; no credential lookup or login flow. |
| `pr create` | Explicit `--head`, `--base`, `--title`, and `--body` or `--body-file`; optional `--draft`. The head must already be pushed. |
| `issue create` | Explicit title and body/body-file. |
| `pr edit`, `issue edit` | Explicit title and/or body/body-file only. |
| `pr comment`, `issue comment` | New comment, explicit body/body-file. |
| `pr review` | `--comment` and explicit body/body-file only. |
| `repo fork` | Coordinator-selected fork owner/name; upstream clone/remote flags remain available. |

Repository overrides remain available. Unsupported commands and changed flags fail
before dispatch, including metadata, attachments, browser writes, approve/merge,
comment edit/delete, PR base edits, fork customization, and implicit PR pushes.
Help, version, and shell completion generation remain available. The executable
manifest is in `pkg/cmd/root/workspace.go`; update it and its tests together.

## Manager wire contract

All requests use the logical API authority `api.github.com` on the socket. The
initial `GET /_chai/capabilities` must return HTTP 200 and JSON:

```json
{"protocol":"chai.github-proxy/v1","ready":true,"generation":"opaque-generation","host":"github.com","login":"chai-bot","operations":["pull_request.create","issue.create","comment.create","pull_request.review","pull_request.edit","issue.edit","repository.fork"]}
```

Capabilities are cached for the process. Every subsequent request carries
`X-Chai-GH-Protocol` and `X-Chai-Workspace-Generation`. The manager must reject stale
generations on every request after restart/reinitialization. Missing/incompatible
capabilities or an unavailable socket cause failure without direct-network fallback.

Writes use `POST /_chai/operations/{operation}` with
`{"parameters":{...}}`. Parameter schemas:

| Operation | Parameters | Required result fields |
| --- | --- | --- |
| `pull_request.create` | `host`, `repository`, `fields: {title, body, headRefName, baseRefName, draft, maintainerCanModify: true}` | `id`, `url` |
| `issue.create` | `host`, `repository`, `fields: {title, body}` | `id`, `url` |
| `comment.create` | `host`, `subject_id`, `body` | `url` |
| `pull_request.review` | `host`, `repository`, `subject_id`, `fields: {body, event: "COMMENT"}` | Completion result object |
| `pull_request.edit`, `issue.edit` | `host`, `repository`, `subject_id`, `fields` containing only changed `title`/`body` | Completion result object |
| `repository.fork` | `host`, `repository`, `fields: null` | `id`, `name`, `owner.login`, `createdAt` (RFC3339) |

`repository` is `owner/name`; IDs are GitHub node IDs. The manager must resolve IDs
and head refs and verify their repository relationship. Omitted edit fields mean
unchanged; an empty body means clear it. Successful operations must return HTTP 200
and `{"status":"completed","result":{...}}`. Pending/accepted responses are not
success. Failures use non-200 status and
`{"error":{"code":"CHAI_GH_APPROVAL_REQUIRED","message":"Actionable handoff instructions"}}`.
Other stable codes include `CHAI_GH_UNSUPPORTED`, `CHAI_GH_DENIED`,
`CHAI_GH_NOT_READY`, `CHAI_GH_CREDENTIAL_UNAVAILABLE`, and `CHAI_GH_UPSTREAM_UNAVAILABLE`.
Native API reads retain GitHub error formats: REST `message` and GraphQL `errors`
must contain the actionable manager code/message when policy rejects a request.
Include request IDs, completed-resource URLs, and partial outcomes in error messages
when available. A malformed or lost write response is uncertain; the CLI does not
retry, follow write redirects, or fall back to a native mutation.

The manager adapts these operations to existing authorized coordinator operations:
`scm_ensure_fork`, `scm_create_change_request`, `scm_edit_change_request`,
`scm_create_issue`, `scm_edit_issue`, and existing comment/review wrappers. It owns
credentials, persona/task identity, authorization, approvals, attribution, content
checks, fork naming, and result normalization. It must not advertise an operation
until it can preserve the complete parameter semantics, including clearing bodies.
Neither the descriptor, headers, CLI version, nor the compatibility manifest grants
access. An unmodified CLI or raw socket client must face the same manager policy.

## Build and maintain

```sh
bash script/build-workspace.sh                   # bin/gh and bin/gh.provenance
bin/gh --version
go test ./...
make lint
node script/workspace-smoke.mjs bin/gh           # Optional Linux/Node 20+ binary fixture
```

The build uses the Go toolchain in `go.mod`, module checksums, `-trimpath`, no embedded
VCS paths, and a deterministic `SOURCE_DATE_EPOCH` (HEAD timestamp by default).
Provenance records source/file digests, dirty state, toolchain, target and binary
digest. Uncommitted builds include a source digest in their version. Build twice to
different paths under ignored `bin/` and compare SHA-256 before publishing a release.
Retain upstream license notices. Do not distribute dirty development builds as a
qualified workspace release.

Maintain four small patch areas: workspace config/transport and diagnostics,
remote identity, typed write adapters, and build provenance. On each upstream
upgrade, review these seams, update the pinned base/version in the build script,
run upstream unit tests plus the socket/command fixtures, and retire patches where
upstream provides equivalent behavior. Live non-root manager integration, policy
parity, signed Git flows, and deployment qualification belong to the gateway rollout.

For a shell that exports noninteractive defaults, run tests with
`env -u GH_PAGER -u PAGER -u NO_COLOR TERM=xterm-256color go test ./...`.
Upstream prompt/color tests require those terminal conditions. The binary fixture
uses a separate home/config, no real credentials or keyring, and a Git wrapper that
stops before network fetch/push/clone. It covers implicit repository discovery, PR
checkout delegation, all seven typed operations, auth diagnostics, pagination, and
pending-write rejection without creating real GitHub resources.

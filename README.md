# wln

`wln` is a small, deterministic CLI for exporting telemetry from Wialon Hosting
or Wialon Local to analysis-ready CSV. It also provides profile management,
unit discovery, and authenticated access to arbitrary Remote API services.

The implementation uses only the Go standard library. It talks to Wialon only
through the documented Remote API and never prints access tokens or session IDs.

## Install

Download the archive for your platform from
[the latest GitHub release](https://github.com/Shooa/wln/releases/latest):

| Platform | Intel/AMD 64-bit | ARM 64-bit |
| --- | --- | --- |
| Linux | `linux_amd64.tar.gz` | `linux_arm64.tar.gz` |
| macOS | `darwin_amd64.tar.gz` | `darwin_arm64.tar.gz` |
| Windows | `windows_amd64.zip` | `windows_arm64.zip` |

Each release includes `SHA256SUMS`. Extract the archive and put `wln` (or
`wln.exe`) somewhere on `PATH`.

Alternatively, with Go 1.23 or newer:

```sh
go install github.com/Shooa/wln/cmd/wln@latest
```

For a repository-local binary:

```sh
make build
./bin/wln --version
```

## Log in and create a profile

The normal login flow opens Wialon's authorization page in the system browser,
listens on a one-time loopback callback, verifies the issued token with
`token/login`, and then saves the profile:

```sh
wln profile login hosting \
  --server https://hosting.wialon.com \
  --default
```

`--server` is the base address of the Wialon installation containing
`login.html`, not necessarily its Remote API address. Wialon Hosting returns
`wialon_sdk_url` in the callback, so a login through `hosting.wialon.com`
automatically stores `hst-api.wialon.com`. Wialon Local falls back to the same
installation base address.

The default access value is `768` (`0x100 + 0x200`): online/message access plus
viewing connectivity properties such as the unit unique ID. The default token
duration is unlimited (`0`), though Wialon removes tokens after 100 days of
inactivity. Useful options:

```sh
wln profile login local \
  --server https://wialon.example.test \
  --user operator \
  --lang ru \
  --duration 720h \
  --callback-timeout 10m
```

Use `--no-open` when the URL should be opened manually. The callback still must
reach the same machine where `wln` is running.

### Manual token fallback

If browser login is unavailable, do not put the token directly in a shell
argument. Supply it through a temporary environment variable:

```sh
read -s WLN_TOKEN
export WLN_TOKEN
wln profile add hosting --server https://hst-api.wialon.com --default
unset WLN_TOKEN
```

Or pipe it on standard input:

```sh
security find-generic-password -w -s wialon-token |
  wln profile add hosting --token-stdin --default
```

Profiles are stored in the OS user configuration directory. The directory is
created with mode `0700` and the JSON file with mode `0600`. `profile list`
never shows tokens:

```sh
wln profile list
wln profile use hosting
wln profile check hosting
wln profile remove old-profile
```

Use `--profile NAME` before the command to override the default profile:

```sh
wln --profile staging units list
```

## Find a unit

```sh
wln units list
wln units list --search 'Truck*'
wln units list --format json
```

`--search` is a Wialon **unit-name mask**, not an IMEI search. JSON output uses
stable CLI field names. Human-readable table output resolves Wialon hardware
IDs through `core/get_hw_types` and uses Unicode borders:

```text
┌──────┬──────────┬─────────────────┬───────────┐
│ ID   │ NAME     │ UNIQUE ID       │ HARDWARE  │
├──────┼──────────┼─────────────────┼───────────┤
│ 1001 │ Truck 01 │ 123456789012345 │ Tracker X │
└──────┴──────────┴─────────────────┴───────────┘
```

JSON retains both the readable name and raw Wialon ID:

```json
[
  {
    "id": 1001,
    "name": "Truck 01",
    "unique_id": "123456789012345",
    "hardware": "Tracker X",
    "hardware_id": 42
  }
]
```

### Unit status and stale positions

Show connection state, the last coordinate, its age, the last message, and the
last known position:

```sh
wln units status
wln units status 1001
wln units status --offline --stale 30d --sort age --limit 20
wln units status --offline --inactive 30d --sort age
```

The default `age` order puts units with no known position first, followed by
the oldest positions. `--stale` is based on the coordinate timestamp, not the
last arbitrary message timestamp, so the two are shown separately. For reuse
candidates, `--inactive` is safer: it requires both the last position and the
last message to be older than the threshold.

```text
The table also includes the current unique ID, making the selected unit
unambiguous before its connectivity settings are changed.
```

## Export messages

The unit argument accepts an exact Wialon ID, exact name, or exact unique
ID/IMEI. Timestamps must be ISO 8601/RFC 3339 values with explicit offsets.

With no interval or output options, `wln` exports from local midnight through
the current time and creates `wialon-UNIQUE_ID-YYYY-MM-DD.csv` in the current
directory:

```sh
wln messages get 1001
```

Before loading messages, `wln` prints a copyable multiline command containing
the resolved interval and output path. Use the explicit form to adjust either
boundary:

```sh
wln messages get 1001 \
  --from 2026-07-19T11:00:00+05:00 \
  --to 2026-07-19T12:00:00+05:00 \
  --batch-size 10000 \
  --output /tmp/wialon-123456789012345.csv
```

Relative and calendar intervals are also supported:

```sh
wln messages get 1001 --last 2h
wln messages get 1001 --last 7d
wln messages get 1001 --today
wln messages get 1001 --yesterday
wln messages get 1001 --since 08:30
```

Write structured data to stdout for pipelines, or retain only selected message
parameters:

```sh
wln messages get 1001 --last 30m --format ndjson --output -
wln messages get 1001 --today --format json --params temperature,voltage
```

By default, only telemetry/data messages are selected. Pass `--all-types` to
include events, commands, logs, and other Wialon message types. Existing output
is not replaced unless `--force` is specified.

The exporter:

1. resolves the unit without guessing;
2. logs in with `token/login`;
3. loads the complete interval through `messages/load_interval`;
4. reads the server-side loader in `--batch-size` pages;
5. flattens nested fields without renaming them (`pos.x`, `p.sensor_temp`, etc.);
6. writes CSV atomically and verifies the returned row count;
7. unloads messages and logs out even after most failures.

Preferred columns are ordered first when present:

```text
t,r,rt,tp,f,i,o,pos.x,pos.y,pos.z,pos.s,pos.c,pos.sc,...sorted dynamic fields
```

All discovered parameters are retained as `p.<name>` columns. CSV, JSON, and
NDJSON files are built through a private temporary spool so large exports do
not have to remain in memory and partially written destination files are not
published.

### Latest messages

```sh
wln messages tail 1001
wln messages tail 1001 -n 100 --format ndjson
wln messages tail 1001 --follow --poll 2s
```

`--follow` polls for new messages until interrupted. Use `--all-types` to
include events, commands, logs, and other non-telemetry messages.

### Native Wialon export

Download formats produced directly by Wialon without converting them:

```sh
wln messages export 1001 --today --format wln
wln messages export 1001 --last 24h --format kml
wln messages export 1001 --yesterday --format wlb --compress
```

Supported formats are `txt`, `kml`, `plt`, `wln`, and `wlb`.

## Diagnose a profile

```sh
wln doctor
wln --profile staging doctor
wln profile check hosting
```

The diagnostic checks the configuration, selected profile, API login latency,
server time, authenticated user, and accessible unit count without displaying
the token or session ID.

## Raw API fallback

For fields not covered by `units` or `messages`, call an authenticated Wialon
service while still using the configured profile:

```sh
wln api call core/search_items --params @request.json
wln api call user/get_locale --params '{}'
```

The response is pretty-printed JSON. Use `--compact` for a compact response.
Credential-management services are blocked, and credential-like response fields
are redacted. `token/login` and `core/logout` are managed internally.

## Global options

```text
--profile NAME
--config PATH
--timeout 2m
--version
```

Global options must precede the top-level command. Command-specific options may
follow their positional argument, matching the examples above.

## API references

- [Remote API introduction and POST request format](https://help.wialon.com/en/api/user-guide)
- [Getting an access token with `login.html` and `redirect_uri`](https://help.wialon.com/en/api/user-guide/getting-an-access-token)
- [Token access flags](https://help.wialon.com/en/api/user-guide/data-format/tokens)
- [`token/login`](https://help.wialon.com/en/api/user-guide/api-reference/token/login)
- [`core/search_items`](https://help.wialon.com/en/api/user-guide/api-reference/core/search_items)
- [`core/get_hw_types`](https://help.wialon.com/en/api/user-guide/api-reference/core/get_hw_types)
- [`messages/load_interval`](https://help.wialon.com/en/api/user-guide/api-reference/messages/load_interval)
- [`messages/load_last`](https://help.wialon.com/en/api/user-guide/api-reference/messages/load_last)
- [`messages/get_messages`](https://help.wialon.com/en/api/user-guide/api-reference/messages/get_messages)
- [`exchange/export_messages`](https://help.wialon.com/en/api/user-guide/api-reference/exchange/export_messages)
- [Message data format](https://help.wialon.com/en/api/user-guide/data-format/messages)
- [Remote API limitations](https://help.wialon.com/en/api/user-guide/limitations)

## Development

```sh
go test ./...
go vet ./...
```

Tests use local `httptest` servers and never require a real Wialon token.

# v1.2.0

## Highlights

This release fixes several reliability and durability bugs in the MQTT publish path and the connection lifecycle, hardens the HTTP stats endpoints, and adds a comprehensive test suite. It encompasses everything since v1.0.0.

The daemon now reports its version at startup and via the `/stats` endpoint.

## What to change when deploying from v1.0.0/v1.1.x

**TL;DR — rebuild, drop in place, restart. No config migration needed.**

### Required steps

1. Rebuild the binary: `go build -o sia2mqtt ./sia2mqtt.go`
2. Replace `/usr/local/bin/sia2mqtt` (or wherever the binary lives).
3. Restart the service: `systemctl restart sia2mqtt`.

`/etc/sia2mqtt.conf`, `sia2mqtt.service`, the state file, and the MQTT topic layout are all backwards-compatible.

### Optional new config keys (all default to the previous behavior)

```ini
sia_verify_crc=false                  # set true for stricter incoming-frame CRC + LEN integrity checks
sia_max_concurrent_connections=10     # was hardcoded; tune if you have many hubs
sia_read_timeout_seconds=60           # per-iteration read deadline (×3 = total idle before disconnect)
```

If you don't add them, defaults match the previous hardcoded values exactly.

### Behavior changes that *could* be visible to downstream consumers

| Change | Who notices | Action |
|---|---|---|
| `last_event_raw` in `/stats` is now redacted (`#REDACTED` in place of the account ID) | Anything parsing that field | Update parser if you rely on the account being there |
| One extra MQTT publish per (re)connect refreshes retained state/user | HA / subscribers (idempotent for retained values) | None expected — this is the bug fix |
| HTTP `/health`, `/state`, `/stats` return 405 + `Allow: GET, HEAD` for non-GET methods | Anything probing with POST/PUT/etc. | Use `GET` (or `HEAD`) |
| Invalid SIA frames no longer log unconditionally — gated behind `verbose=true`; instead bumped in `stats.invalid_frames` | Log greppers looking for `DEBUG: frame did not match` | Switch to monitoring `/stats.invalid_frames` |
| Concurrent-connection limit now **rejects** with connection-close + `stats.rejected_flood++` instead of blocking accept | Ajax hubs seeing connection-refused under flood | None (limit is still 10 by default) |

### New `/stats` fields (additive, safe to ignore)

`version`, `invalid_frames`, `rejected_flood`, `mqtt_reconnects`

### Rollback

The change is entirely in the daemon binary plus optional config additions. To roll back, redeploy the previous binary; the old binary will reject the three new keys with `unknown key`, so if you added them, comment them out or use the old conf.

## Reliability fixes

- **Failed MQTT publishes no longer poison dedup.** Previously, `PublishState` and `PublishUser` updated their dedup cache *before* attempting the publish. If MQTT was disconnected (or the publish errored), the broker never received the message but the publisher believed it did, so the next identical event was silently dropped. Dedup is now updated only after a successful publish.
- **MQTT reconnect republishes current state and user.** The `OnConnect` callback now calls a new `RepublishCurrent()` so subscribers and broker-restart scenarios see the latest values, not stale or missing retained data.
- **Daemon restart republishes the persisted state.** Previously, the persisted state file was only used to seed the dedup cache; nothing was published until the next SIA event arrived. Now the loaded state is re-sent to the broker on first connect.
- **Publisher mutex released before `Publish().Wait()`.** A slow broker can no longer serialize every state and user publish through one mutex.

## Durability fixes

- **`persistState` is now crash-durable.** The temp file is `Sync()`'d before close and the parent directory is `fsync`'d after rename, so a power loss between `rename(2)` and the metadata flush cannot lose the data.

## Connection lifecycle

- **Non-blocking flood reject.** When the concurrent-connection limit is reached, new connections are now closed immediately (and counted as `rejected_flood`) instead of blocking the accept loop with a half-attached socket the peer thinks is established.
- **Transient `Accept` errors no longer kill the daemon.** A single `EMFILE`-class error used to break the accept loop and trigger shutdown. Now the loop distinguishes `net.ErrClosed` (real shutdown) from transient errors, logs the latter, and backs off 250 ms before retrying.
- **Graceful drain on shutdown.** Active connection handlers are tracked in a `sync.WaitGroup` and given up to 5 s to finish in-flight frames before MQTT, HTTP, and state are torn down.
- **Configurable connection limits.** `sia_max_concurrent_connections` and `sia_read_timeout_seconds` are no longer hardcoded.

## Protocol & validation

- **`reEventCode` regex widened.** User/zone IDs from 1 to 4 digits are now captured. Previously the regex required exactly 3 digits, silently truncating 4-digit IDs (e.g. `OP1234` → user `123`) and dropping 2-digit IDs entirely.
- **Optional incoming CRC + LEN verification.** Set `sia_verify_crc=true` to verify the leading checksum and length of every incoming frame against the payload before sending an ACK. Off by default for compatibility. Frames that fail integrity check are not ACK'd and counted in `stats.invalid_frames`.
- **`publishDiscoveryConfig` race fixed.** A new `discoveryMu` mutex prevents the `OnConnect` callback and the post-`Connect` fallback from both passing the `discoverySent` check and double-publishing.

## HTTP & observability

- **Method restriction on stats endpoints.** `/health`, `/state`, `/stats` now return `405 Method Not Allowed` with the `Allow: GET, HEAD` header for any other method.
- **HTTP server timeouts.** `ReadTimeout`, `WriteTimeout`, and `IdleTimeout` are now set explicitly (in addition to the existing `ReadHeaderTimeout`), preventing slowloris-style attacks if the endpoint is ever exposed beyond `127.0.0.1`.
- **Account ID redacted from `last_event_raw`.** Stored as `#REDACTED` so `/stats` cannot leak the configured Object Number even if HTTP is exposed beyond localhost.
- **Invalid-frame logging gated behind `verbose`.** Spurious or malicious frames no longer flood the log; the count is exposed via `stats.invalid_frames` instead.
- **New stats counters.** `version`, `invalid_frames`, `rejected_flood`, `mqtt_reconnects`.
- **Consistent log flags.** `log.SetFlags(0)` now applies regardless of whether `log_file` is set.

## Earlier hardening (since v1.0.0)

- Graceful shutdown on `SIGTERM`/`SIGINT`.
- Idle-connection cleanup after 3 consecutive read timeouts.
- Write deadline on outgoing ACKs.
- Robust user-topic derivation (one-time `MQTTBaseTopic` at config load).
- All four `json.Marshal` callsites now check for errors.
- Broker URL sanitization in log lines redacts password from any userinfo.

## Testing

- 105 tests, all passing under `-race`.
- Includes a regression guard: a failed publish must never update the dedup cache.
- New tests for: CRC/LEN verification (valid, bad CRC, bad LEN, too-short, non-hex LEN), account-ID redaction, HTTP method restriction, variable-digit user IDs (1-4), invalid-frame counter, `RepublishCurrent` no-op, `Version` constant.

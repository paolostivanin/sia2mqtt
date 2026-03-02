## sia2mqtt — Ajax SIA DC-09 to MQTT bridge

Small daemon that accepts SIA DC-09 events from an Ajax hub, acknowledges them correctly,
and publishes a simplified alarm state to MQTT for Home Assistant or other consumers.
It also exposes a tiny HTTP endpoint for health/state visibility and keeps a local
state file so restarts do not lose the last known alarm mode.

**What it does**
* Reads config from `/etc/sia2mqtt.conf` (simple `key=value`, `#` comments).
* Listens for SIA DC-09 over TCP on `sia_listen` (default `:45128`).
* For every message from the Ajax hub:
  - Reads a full frame (terminated by `\r` or `\n`).
  - Parses the DC-09 header to extract seq, optional `Rxxxx`, `Lx`, `#acct`.
  - Sends a correct DC-09 ACK back to the hub (with CRC-16/IBM), so the hub keeps the
    connection "green".
  - Extracts the Ajax event code (e.g., `OP502`, `CL502`, `NL502`, `BA502`).
  - Maps that to a state: `disarmed | armed | night | alarm`.
  - Publishes the state to MQTT on `mqtt_topic` (retained if configured).
  - Publishes the user ID to a separate MQTT topic (`<mqtt_topic_base>/user`).
  - Persists the last known state to `state_file` (default:
    `/var/lib/sia2mqtt/state.json`).
* HTTP endpoints:
  - `/health` — plain text `ok`
  - `/state` — current alarm state + last event time (JSON)
  - `/stats` — full runtime stats including MQTT status, frame counters, uptime (JSON)
* MQTT availability (LWT):
  - Publishes `online` to `mqtt_availability_topic` on connect.
  - Sets LWT to publish `offline` if the daemon dies or loses connection unexpectedly.
* Home Assistant MQTT Discovery:
  - Publishes auto-discovery config for two sensors: alarm state + last user.
  - Re-publishes discovery on MQTT reconnect.

## Build

Requires Go 1.24+.

```bash
go build -o sia2mqtt ./sia2mqtt.go
```

## Configuration

Copy the example config and edit it:

```bash
sudo install -m 0640 ./sia2mqtt.conf /etc/sia2mqtt.conf
sudo nano /etc/sia2mqtt.conf
```

At minimum you must set:
- `sia_account_id` — your Ajax "Object Number"
- `mqtt_broker` — your MQTT broker address
- `mqtt_user` / `mqtt_password` — if your broker requires authentication

See `sia2mqtt.conf` for all available options with descriptions.

## Run

```bash
# With explicit config path
./sia2mqtt /etc/sia2mqtt.conf

# Uses /etc/sia2mqtt.conf by default
./sia2mqtt
```

## Installation (systemd)

1. **Create a dedicated user (recommended)**

```bash
sudo useradd --system --home /var/lib/sia2mqtt --shell /usr/sbin/nologin sia2mqtt
```

2. **Install the binary**

```bash
sudo install -m 0755 ./sia2mqtt /usr/local/bin/sia2mqtt
```

3. **Install and edit the config**

```bash
sudo install -m 0640 ./sia2mqtt.conf /etc/sia2mqtt.conf
sudo nano /etc/sia2mqtt.conf
```

4. **Create the state and log directories**

```bash
sudo install -d -o sia2mqtt -g sia2mqtt -m 0755 /var/lib/sia2mqtt
sudo install -d -o sia2mqtt -g sia2mqtt -m 0755 /var/log/sia2mqtt
```

5. **Install the systemd unit**

```bash
sudo install -m 0644 ./sia2mqtt.service /etc/systemd/system/sia2mqtt.service
```

6. **Update the service user**

Edit `/etc/systemd/system/sia2mqtt.service` and set:

```ini
User=sia2mqtt
Group=sia2mqtt
```

Also adjust `ReadWritePaths` if your `state_file` or logs live elsewhere.

7. **Enable and start**

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now sia2mqtt.service
```

## Ajax Hub Setup

In your Ajax app, configure the monitoring station (SIA protocol):

1. Go to **Hub Settings → Monitoring Station**
2. Set **Protocol** to **SIA (DC-09)**
3. Set **Server address** to the IP of the machine running sia2mqtt
4. Set **Server port** to `45128` (or your configured `sia_listen` port)
5. Set the **Object Number** — this must match `sia_account_id` in your config

## MQTT Topics

With default config (`mqtt_topic=home/alarm/ajax/state`):

| Topic | Payload | Retained |
|---|---|---|
| `home/alarm/ajax/state` | `{"state":"armed","code":"CL","ts":"..."}` | yes |
| `home/alarm/ajax/user` | `{"user":"502","ts":"..."}` | yes |
| `home/alarm/ajax/availability` | `online` / `offline` | yes |

Alarm states: `unknown`, `disarmed`, `armed`, `night`, `alarm`.

## License

MIT

# POKE 443 - A simple Infrastructure Monitoring Tool

POKE 443 is a simple utility service that periodically runs health checks (Ping, HTTP or TCP) against a list of hosts from a YAML/TOML config, shows live status in a built-in web UI, and optionally notifies Healthchecks.io and/or publishes state changes to MQTT.

## Whats with the name?

POKE is a basic instruction that pokes a single byte value directly into a specific memory location. It was famously used on systems like the ZX Spectrum to modify memory to give players infinite lives in games and such like.  If you issued a ```POKE 35899,0``` instruction before playing [Manic Miner](https://en.wikipedia.org/wiki/Manic_Miner) would give the player infinite lives. 

This application 'POKES' configured systems to see if they are alive. It may POKE port 443 for example. I like the name and I liked the call back.

## Features
- Hosts defined in config with one or more checks per host
- Checks: ping, tcp port open, http (with expected status code)
- Enable/disable checks per host
- Web UI to add/edit/delete hosts and add/remove/update checks
- “Unknown” status until a host’s checks run the first time
- Optional Healthchecks.io ping URL per host for notifications
- Check dependencies
- MQTT integration

Everything compiles to a single binary for easy deployment

## Build
- go build ./cmd/poke443
- Binary: ./poke443

## Run

To run in the terminal:
- ```./poke443 -config config.yaml -addr :8080 ```

To run as a systemtray / menu item:
- ```./poke443 -menubar -config config.yaml -addr :8080 ```

To access the dashboard:

- Open http://localhost:8080

## Command-line options
- -config string      Path to config file (YAML or TOML). Default: config.yaml
- -addr string        HTTP listen address. Default: :8080
- -interval duration  Check interval (e.g. 30s, 1m). Default: 30s
- -log string         Path to log file (optional; defaults to stderr)
- -http-log           Enable web server request logging (disabled by default)
- -menubar            Forks the process and provides a menubar icon to manage the app

On start, the app logs: “poke443 started; web UI listening on <addr>”.

## Configuration
YAML example (see config.example.yaml for a fuller sample):

```yaml
hosts:
  # Define a gateway/internet connectivity check first
  - name: "Internet Gateway"
    address: "8.8.8.8"
    checks:
      - type: ping
        enabled: true
        id: "internet"  # Unique ID that other checks can depend on

  - name: "router"
    address: "192.168.1.1"
    checks:
      - type: ping
        enabled: true
        mqtt_notify: true  # Send MQTT notification when state changes
    # Optional: Healthchecks.io ping URL (https://hc-ping.com/<uuid>)
    # On success we GET <url>, on failure we GET <url>/fail
    healthchecks_ping_url: "https://hc-ping.com/00000000-0000-0000-0000-000000000000"

  - name: "example"
    address: "example.com"
    checks:
      - type: ping
        enabled: true
        depends_on: "internet"  # If internet check is down, this won't alert
      - type: http
        url: "https://example.com/"
        expect: 200
        enabled: true
        depends_on: "internet"  # If internet check is down, this won't alert
        mqtt_notify: true  # Send MQTT notification when state changes
      - type: tcp
        port: 443  # Check if HTTPS port is open
        enabled: true
        depends_on: "internet"

# MQTT Settings (optional)
# Configure MQTT broker to receive notifications on state changes
settings:
  mqtt:
    enabled: false
    broker: "tcp://localhost:1883"
    client_id: "simple-healthchecker"
    username: ""
    password: ""
    topic: "healthchecker"  # Messages published to: <topic>/state-change
```

## Usage Notes

- check type ping has no URL or expect; just enabled flag.
- check type http requires url; expect is optional (defaults to 200).
- check type tcp require a TCP port to probe
- healthchecks_ping_url is optional per host. If set, failures will be reported and recoveries can be marked OK.
- id: optional unique identifier for a check that other checks can depend on
- depends_on: ID of a parent check. If the parent is down, this check shows "blocked" instead of alerting
- Each check can be set to publish state changes on MQTT. If MQTT is configured

## Check Dependencies

Check dependencies allow you to set up parent-child relationships between checks. When a parent check fails, all dependent checks are marked as "blocked" instead of "down", and alerts are suppressed.

**Use case:** You have an "internet connectivity" check (ping to 8.8.8.8). External website checks depend on it. If your internet goes down, you see only one alert ("internet is down") instead of dozens of alerts for every external service.

```yaml
hosts:
  - name: "Internet"
    address: "8.8.8.8"
    checks:
      - type: ping
        id: "internet"  # Parent check
        enabled: true
        
  - name: "Google"
    address: "google.com"
    checks:
      - type: ping
        depends_on: "internet"  # Child check
        enabled: true
```

When the "internet" check fails:
- The "internet" check shows as **Down** (red)
- The "Google" check shows as **Blocked** (orange)
- Only the parent failure triggers alerts/events

TOML uses equivalent keys.

## Web UI
- Cards show each host, its checks, last status (UP/DOWN/UNKNOWN), latency, and last-checked time.
- Edit dialog lets you:
  - Change host name/address and Healthchecks.io URL
  - Add new checks (Ping/HTTP/TCP) and remove existing checks
  - For HTTP checks: set target URL and expected status code
- Main view keeps card order stable and auto-refreshes periodically.

## Healthchecks.io integration
- Set healthchecks_ping_url on a host to enable notifications.
- The service will call Healthchecks.io endpoints based on check outcomes.

## Logging
- By default logs to stderr; use -log /path/app.log to write to a file.
- Use -http-log to add request logs for the web UI endpoints.

## ICMP on macOS
- Standard raw-ICMP requires privileges on macOS. This app includes a Darwin-specific option to perform ping checks without requiring root. If ping checks fail due to permissions, ensure you’re on the latest build of this app.

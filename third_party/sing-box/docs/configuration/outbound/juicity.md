---
icon: material/new-box
---

!!! question "Since sing-box 1.13.4"

### Structure

```json
{
  "type": "juicity",
  "tag": "trusttunnel-out",

  "server": "127.0.0.1",
  "server_port": 443,
  "uuid": "bf000d23-0752-40b4-affe-68f7707a9661",
  "password": "tunnel",
  "tls": {},

  ... // Dial Fields
}
```

### Fields

#### server

==Required==

The server address.

#### server_port

==Required==

The server port.

#### uuid

==Required==

Authentication uuid.

#### password

Authentication password.

#### tls

==Required==

Outbound TLS configuration, see [TLS](/configuration/shared/tls/#outbound).

### Dial Fields

See [Dial Fields](/configuration/shared/dial/) for details.

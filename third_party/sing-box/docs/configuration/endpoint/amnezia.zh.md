---
icon: material/new-box
---

!!! question "Since sing-box X.Y.Z"

### Structure

```json
{
  "type": "amneziawg",
  "tag": "awg-out",

  "private_key": "YOUR_PRIVATE_KEY",
  "address": [
    "10.0.0.2/32"
  ],
  "mtu": 1420,
  "listen_port": 51820,

  "jc": 5,
  "jmin": 50,
  "jmax": 100,
  "s1": 15,
  "s2": 20,
  "s3": 25,
  "s4": 30,

  "h1": "1:2:3",
  "h2": "4:5:6",
  "h3": "7:8:9",
  "h4": "10:11:12",

  "i1": "value1",
  "i2": "value2",
  "i3": "value3",
  "i4": "value4",
  "i5": "value5",

  "peers": [
    {
      "address": "example.com",
      "port": 51820,
      "public_key": "SERVER_PUBLIC_KEY",
      "preshared_key": "PRESHARED_KEY",
      "allowed_ips": [
        "0.0.0.0/0",
        "::/0"
      ],
      "persistent_keepalive_interval": 25
    }
  ],

  ... // Dial Fields
}
```

### Fields

#### private_key

==Required==

Local private key used for authentication.

#### address

==Required==

List of IP prefixes assigned to the local interface.

#### mtu

Interface MTU.

#### listen_port

Local UDP listen port.

#### jc

AmneziaWG junk packet count parameter.

#### jmin

Minimum delay between junk packets.

#### jmax

Maximum delay between junk packets.

#### s1

AmneziaWG protocol obfuscation parameter S1.

#### s2

AmneziaWG protocol obfuscation parameter S2.

#### s3

AmneziaWG protocol obfuscation parameter S3.

#### s4

AmneziaWG protocol obfuscation parameter S4.

#### h1

AmneziaWG protocol obfuscation parameter H1.

#### h2

AmneziaWG protocol obfuscation parameter H2.

#### h3

AmneziaWG protocol obfuscation parameter H3.

#### h4

AmneziaWG protocol obfuscation parameter H4.

#### i1

AmneziaWG protocol obfuscation parameter I1.

#### i2

AmneziaWG protocol obfuscation parameter I2.

#### i3

AmneziaWG protocol obfuscation parameter I3.

#### i4

AmneziaWG protocol obfuscation parameter I4.

#### i5

AmneziaWG protocol obfuscation parameter I5.

#### peers

List of remote peers.

### Peer Fields

#### address

Peer endpoint address.

#### port

Peer endpoint port.

#### public_key

==Required==

Peer public key.

#### preshared_key

Optional WireGuard pre-shared key.

#### allowed_ips

List of IP prefixes routed through this peer.

#### persistent_keepalive_interval

Persistent keepalive interval in seconds.

### Dial Fields

See [Dial Fields](/configuration/shared/dial/) for details.

---
icon: material/new-box
---

!!! question "自 sing-box 1.13.4 起"

### 结构

```json
{
  "type": "juicity",
  "tag": "juicity-out",

  "server": "127.0.0.1",
  "server_port": 443,
  "uuid": "bf000d23-0752-40b4-affe-68f7707a9661",
  "password": "tunnel",
  "tls": {},

  ... // Dial Fields
}
```

### 字段

#### server

==必填==

服务器地址。

#### server_port

==必填==

服务器端口。

#### uuid

==必填==

认证用户名。

#### password

认证密码。

#### tls

==必填==

出站 TLS 配置，参阅 [TLS](/zh/configuration/shared/tls/#outbound)。

### 拨号字段

拨号字段参阅 [拨号字段](/zh/configuration/shared/dial/)。

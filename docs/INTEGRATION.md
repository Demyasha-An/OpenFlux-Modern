# Connecting Android / desktop to OpenFlux

OpenFlux exposes a standard SOCKS5 server, so any client that speaks SOCKS5
works: INCY, v2rayNG/Hiddify, Firefox, Telegram Desktop, curl, qBittorrent, ...

## Android (INCY)

1. **Exit node** (VPS, `/opt/OpenFlux-Modern`):

   ```bash
   cd /opt/OpenFlux-Modern
   # .env:
   #   TRANSPORT=vyandex
   #   DOC_URL=https://disk.yandex.ru/i/<your-doc>
   #   MODE=l4
   #   RESOLVE=volga.yandex.ru=77.88.21.171,push.yandex.ru=213.180.204.179
   docker compose --profile exit-node up -d
   ```

2. **Client** (Termux) — background so switching apps does not kill it:

   ```bash
   pkill -f openflux; cd ~/OpenFlux && git pull && \
   go build -ldflags="-checklinkname=0" -o openflux . && \
   nohup ./openflux --client --transport vyandex --mobile --dns doh \
     --url "https://disk.yandex.ru/i/RyldS7an2uECIg" \
     --resolve "volga.yandex.ru=77.88.21.171,push.yandex.ru=213.180.204.179,disk.yandex.ru=87.250.250.50,docs.yandex.ru=87.250.250.50" \
     --socks5 127.0.0.1:1080 --debug > ~/openflux.log 2>&1 &
   ```

   Flags that matter on a phone:
   - `--mobile` — light relay workers/queues, small gVisor TCP buffers, DoT for Yandex hosts.
   - `--resolve` — Yandex anycast IPs. Several IPs per host are allowed, and the
     system resolver is the fallback, so a rotated address no longer bricks the tunnel.
   - `--dns doh` — **resolve SOCKS5 hostnames over DoH through the tunnel itself**
     (INCY passes bare hostnames; the operator DNS is poisoned). This removes the
     remaining DNS failure mode without depending on DoT/853.

3. **INCY**: add a SOCKS5 profile `127.0.0.1:1080` (no auth), enable it, then
   start the hotspot. Exclude the Termux/OpenFlux app from the VPN so the client
   does not tunnel its own relay traffic.

Check:

```bash
curl -4 --socks5-hostname 127.0.0.1:1080 --max-time 20 https://ifconfig.me
```

Expect the VPS address, not the mobile one.

## Laptop as the client (fastest way to debug)

```bash
go build -trimpath -o openflux.exe .
./openflux.exe --client --transport vyandex --url "https://disk.yandex.ru/i/RyldS7an2uECIg" --socks5 127.0.0.1:1080 --debug
```

Then point any app at `socks5://127.0.0.1:1080` (or `socks5h://` to force
remote DNS). To let other LAN devices (e.g. a phone) use the laptop as the
tunnel, listen beyond loopback and set credentials:

```bash
./openflux.exe --client ... --socks5 0.0.0.0:1080 --socks5-auth myuser:mypass
```

then use `socks5h://myuser:mypass@<laptop-ip>:1080` from the other device.

## Remnawave / Xray

Remnawave issues Xray/VLESS configs; OpenFlux is a SOCKS5 exit, so the two
compose instead of competing: run OpenFlux as the client, then point an Xray
outbound at its SOCKS5 port and let Xray own the system VPN slot.

1. Start OpenFlux (client) as above with `--dns doh`.
2. In the Xray client config (Hiddify / v2rayNG custom config, or a
   Remnawave-generated config you edit), chain the outbound through OpenFlux:

   ```json
   {
     "outbounds": [
       { "protocol": "socks", "settings": { "servers": [
           { "address": "127.0.0.1", "port": 1080 } ] } },
       { "protocol": "freedom", "tag": "direct" }
     ],
     "routing": {
       "rules": [
         { "type": "field", "inboundTag": ["socks-in"], "outboundTag": "direct" }
       ]
     }
   }
   ```

   The first outbound is the chain hop: app -> Xray -> OpenFlux SOCKS -> exit node.
   The `direct` freedom outbound is used for traffic that should bypass the chain
   (e.g. the Remnawave subscription host itself, so the panel keeps working).

3. If Remnawave is remote, add a routing rule so panel/API domains go out via
   `direct` and everything else uses the SOCKS outbound. That keeps subscription
   updates working while user traffic exits through the VPS.

This composition needs no changes on the exit node: it only ever sees SOCKS5
traffic from the local Xray.

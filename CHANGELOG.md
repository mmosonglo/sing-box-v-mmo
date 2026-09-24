# Changelog — sing-box v1.12.25 Custom (OpenWrt Pure Lightweight)

Tất cả thay đổi so với upstream [sing-box v1.12.25](https://github.com/SagerNet/sing-box/releases/tag/v1.12.25).

## [v1.12.25-custom-stable] — 2026-09-18

### Added
- **Smart Memory Reclaimer Agent** (`common/autotune/inspector.go`):
  Goroutine tự động chạy `debug.FreeOSMemory()` mỗi 10 giây, ép Go runtime
  trả RAM thừa về cho Linux kernel. Giảm VmRSS từ ~24MB xuống ~17.2MB.
- **Go runtime tuning cho thiết bị nhúng**:
  - `GOGC=50` — GC tích cực hơn (mặc định 100).
  - `GOMEMLIMIT=32MB` — giới hạn mềm tổng heap.
- **GitHub Actions CI** (`.github/workflows/build-openwrt.yml`):
  Tự động build 3 kiến trúc (ARM64, AMD64, MT7621) khi push tag.
- Unit test cho autotune package.

### Removed — Protocol & Endpoint
- **WireGuard** endpoint + outbound (`with_wireguard` tag)
- **Tailscale** endpoint + DNS transport (`with_tailscale` tag)
- **DERP** relay service
- **Tor** outbound
- **SSH** outbound
- **Naive** inbound
- **ShadowTLS** inbound + outbound
- **AnyTLS** inbound + outbound

### Removed — Server-side Inbounds
- Shadowsocks **Server** inbound
- VMess **Server** inbound
- Trojan **Server** inbound
- VLess **Server** inbound

> Router chỉ đóng vai trò **Client** proxy, không cần server inbound.

### Removed — Services & Features
- `systemd-resolved` DNS service
- `ssmapi` service
- **gVisor** userspace network stack (`with_gvisor` tag)
- **QUIC** transports (`with_quic` tag) — bao gồm Hysteria, Hysteria2, TUIC
- **DHCP** DNS transport (`with_dhcp` tag)
- **ACME** auto TLS certificates (`with_acme` tag)
- **ShadowsocksR** stub registrations

### Changed
- **Build tags**: `with_utls,with_clash_api` (từ 8 tags xuống 2)
- **Binary size**: giảm ~50% (32MB → 16-18MB tùy kiến trúc)
- **RAM runtime**: giảm ~30% (24MB → ~17.2MB VmRSS)

### Kept — Core Features
- Shadowsocks outbound (AEAD + 2022)
- VLess outbound (Reality/XTLS/WebSocket/gRPC)
- VMess outbound (WebSocket/gRPC/Mux)
- Trojan outbound (TLS/uTLS)
- SOCKS5 / HTTP outbound
- Direct / Block outbound
- TUN inbound (sing-tun transparent proxy)
- Redirect / TProxy inbound
- Mixed / SOCKS / HTTP inbound
- FakeIP DNS
- DoH / DoT / UDP / TCP DNS transports
- Hosts DNS
- Selector / URLTest group
- uTLS fingerprinting
- Clash API + Rule-set (GeoIP/GeoSite)

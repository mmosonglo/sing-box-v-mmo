---
name: openwrt-singbox-custom
description: Hướng dẫn tích hợp, biên dịch và vận hành phiên bản sing-box 1.12.25 Siêu Nhẹ (16MB RAM ~17.2MB) dành riêng cho Router OpenWrt.
---

# OpenWrt Custom Sing-Box 1.12.25 (Pure Lightweight & Smart Memory Agent)

Tài liệu tổng hợp toàn bộ kiến trúc, thành phần tính năng, cơ chế tối ưu bộ nhớ và hướng dẫn vận hành phiên bản `sing-box 1.12.25` siêu nhẹ dành cho thiết bị nhúng Router OpenWrt.

---

## 1. Tổng quan Kiến trúc & Nhánh Git

* **Repository**: `https://github.com/mmosonglo/sing-box-v-mmo.git`
* **Nhánh hoạt động (Active Branch)**: `v1.12-pure-lightweight`
* **Phiên bản nền**: `sing-box 1.12.25` (Go 1.23.6 / Go 1.24+)

---

## 2. Các Thành phần Tính năng Thực tế Đang Có (Active Features)

### 📤 Giao thức Outbound (Client Proxy)
* **Shadowsocks**: AEAD, Shadowsocks 2022 (`2022-blake3-aes-128-gcm`, `2022-blake3-chacha20-poly1305`).
* **VLess**: TCP, WebSocket, gRPC, TLS/uTLS, XTLS/Reality.
* **VMess**: TCP, WebSocket, gRPC, Mux.
* **Trojan**: Trojan over TLS/uTLS.
* **SOCKS5 & HTTP Outbound**.
* **Direct**: Kết nối trực tiếp, hỗ trợ `bind_interface` ép lưu lượng qua card mạng ảo (ví dụ `tun01`, `tun24`, `eth1`).
* **Block / Reject**: Chặn quảng cáo & gói tin rác.

### 📥 Giao thức Inbound (Cổng nhận trên Router)
* **TUN (`sing-tun`)**: Card mạng ảo Transparent Proxy toàn hệ thống.
* **Redirect / TProxy**: Nhận lưu lượng chuyển hướng từ `iptables` / `nftables` (TCP Redirect & UDP TProxy).
* **SOCKS5 / HTTP / Mixed Inbound**: Mở cổng SOCKS5/HTTP nội bộ (`127.0.0.1:1080` / `1081`).
* **Direct Inbound**.

### 🌐 Hệ thống DNS & Routing
* **DoH (DNS over HTTPS)** & **DoT (DNS over TLS)** & UDP/TCP DNS.
* **FakeIP (FakeDNS)**: Cấp IP ảo (`198.18.0.0/15`) giúp truy cập tức thì, không leak DNS.
* **Hosts & Local DNS**: Khai báo IP tĩnh cho domain (`dns.google.com` ➔ `8.8.8.8`).
* **Selector & URLTest**: Chọn node thủ công hoặc tự chọn node có độ trễ (ping) thấp nhất.
* **uTLS**: Giả lập vân tay TLS (`chrome`, `firefox`, `safari`, `ios`).
* **Clash API & Rule-set**: Nạp bộ lọc theo `geoip` và `geosite`.

---

## 3. Memory Reclaimer Agent (`Smart Memory Reclaimer`)

* **Mã nguồn**: [`common/autotune/inspector.go`](file:///home/mmo/sing-box/common/autotune/inspector.go)
* **Khởi động**: Tự động 100% qua `func init()` trong Go Runtime ngay khi `sing-box` bật.
* **Cơ chế**:
  - **Tuning Go Runtime** ngay từ đầu: `GOGC=50` (GC tích cực hơn) + `GOMEMLIMIT=32MB` (giới hạn mềm heap).
  - Mỗi **10 giây**, Agent gọi `debug.FreeOSMemory()` — hàm này tự chạy GC và trả RAM thừa về cho Linux kernel.
  - **Không dùng `runtime.ReadMemStats()`** — tránh hoàn toàn overhead Stop-the-World trên thiết bị yếu.
* **Hiệu quả thực tế**:
  - Giảm bộ nhớ tiêu thụ thực tế (VmRSS) trên Router từ **24MB** xuống còn **~17.2 MB RAM**.

---

## 4. Các Thành phần Đã Cắt Giảm (Trimmed for Memory)

* **Protocol phụ nặng**: WireGuard, Tailscale, DERP, Tor, SSH, ShadowTLS, Naive, AnyTLS, gVisor, ACME, QUIC.
* **Inbound Server Modules**: VMess/VLess/Trojan/Shadowsocks Server (Router chỉ đóng vai trò Client).
* **Service phụ**: `systemd-resolved` và `ssmapi`.

---

## 5. Bảng Kích thước Binary & Đường dẫn trong Dự án

Toàn bộ binary đã được biên dịch với tham số `-trimpath -ldflags "-s -w -buildid=" -tags "with_utls,with_clash_api"`:

| Kiến trúc Router | Đường dẫn lưu trữ trong Dự án | Kích thước | Ghi chú Biên dịch |
| :--- | :--- | :--- | :--- |
| **ARM64** | [`release/bin/sing-box_1.12.25_openwrt_arm64`](file:///home/mmo/sing-box/release/bin/sing-box_1.12.25_openwrt_arm64) | **16.0 MB** | `GOOS=linux GOARCH=arm64` |
| **AMD64** | [`release/bin/sing-box_1.12.25_openwrt_amd64`](file:///home/mmo/sing-box/release/bin/sing-box_1.12.25_openwrt_amd64) | **17.0 MB** | `GOOS=linux GOARCH=amd64` |
| **MT7621** | [`release/bin/sing-box_1.12.25_openwrt_mt7621`](file:///home/mmo/sing-box/release/bin/sing-box_1.12.25_openwrt_mt7621) | **18.0 MB** | `GOOS=linux GOARCH=mipsle GOMIPS=softfloat` |

---

## 6. Lệnh Biên dịch & Kiểm tra RAM trên Router

### 🛠️ Lệnh biên dịch lại từ nguồn:
```bash
export PATH=$PATH:/home/mmo/.local/go/bin

# ARM64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-X 'github.com/sagernet/sing-box/constant.Version=1.12.25' -s -w -buildid=" -tags "with_utls,with_clash_api" -o release/bin/sing-box_1.12.25_openwrt_arm64 ./cmd/sing-box

# MT7621
CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -trimpath -ldflags "-X 'github.com/sagernet/sing-box/constant.Version=1.12.25' -s -w -buildid=" -tags "with_utls,with_clash_api" -o release/bin/sing-box_1.12.25_openwrt_mt7621 ./cmd/sing-box
```

### 📊 Lệnh kiểm tra RAM tiêu thụ thực tế trên Router:
```bash
for pid in $(pgrep sing-box); do
    echo "PID $pid:"
    grep -i -E 'VmRSS|VmHWM' /proc/$pid/status
done
```

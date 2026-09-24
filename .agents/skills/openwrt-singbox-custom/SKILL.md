---
name: openwrt-singbox-custom
description: Hướng dẫn tích hợp, biên dịch và vận hành sing-box 1.12.25 OpenWrt Bản Chính (Pure Lightweight) — Cổng cất cánh tuần tự và dự báo RAM an toàn, Go Runtime và Routing nguyên bản 100% như repo gốc (Silent Mode, Không dọn rác ngầm, Không kiểm soát kết nối).
---

# OpenWrt Custom Sing-Box 1.12.25 (Bản Chính — Pure Lightweight)

Tài liệu chuẩn hóa kiến trúc và hướng dẫn vận hành phiên bản **`sing-box 1.12.25 OpenWrt Bản Chính`** dành riêng cho người dùng muốn giữ nguyên bản 100% Go Runtime và Routing như upstream, chỉ bổ sung Trạm gác Cất cánh Tuần tự chống tràn RAM khi Passwall2 nạp nhiều node.

---

## 1. Tổng quan Kiến trúc & Nhánh Git

* **Repository**: `https://github.com/mmosonglo/sing-box-v-mmo.git`
* **Nhánh hoạt động (Active Branch)**: `v1.12-pure-lightweight`
* **Phiên bản nền**: `sing-box 1.12.25` (Go 1.23+)
* **Đặc tính cốt lõi**:
  - **Trạm gác Cất cánh Tuần tự (`AcquireStartupGate`)**: Khóa POSIX `flock` tại `/tmp/sing-box-startup.lock` + Dự báo RAM qua `/proc/*/statm`, giải quyết triệt để sự cố "BÙM 1 nhát full RAM" khi Passwall2 khởi động đồng loạt 13–20 node.
  - **Go Runtime Nguyên bản 100%**: Hoàn toàn không can thiệp dọn rác, không ép GOGC, không FreeOSMemory, để Go GC Pacer tự nhiên vận hành chuẩn như repo gốc.
  - **Đường mạng Nguyên bản 100%**: `route/conn.go` giữ nguyên mã nguồn gốc của sing-box, không bóp hay kiểm soát kết nối.
  - **Hoàn toàn Im lặng (Silent Mode)**: Không xuất bất kỳ log rác nào ra syslog OpenWrt.
  - **Kích thước siêu nhẹ**: Binary chỉ ~14.6MB (ARM64).

---

## 2. Hướng dẫn Biên dịch & Kiểm thử

```bash
export PATH=$PATH:/home/mmo/.local/go/bin
go test -v -race ./common/butler/...
make openwrt_all
```

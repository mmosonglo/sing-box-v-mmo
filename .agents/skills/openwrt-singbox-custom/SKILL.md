---
name: openwrt-singbox-custom
description: Hướng dẫn tích hợp, biên dịch và vận hành sing-box 1.12.25 OpenWrt Lite — Cổng cất cánh tuần tự, dự báo RAM an toàn, và dọn rác bộ nhớ thông minh (Silent Mode, Không kiểm soát kết nối).
---

# OpenWrt Custom Sing-Box 1.12.25 (Lite Edition)

Tài liệu chuẩn hóa kiến trúc và hướng dẫn vận hành phiên bản **`sing-box 1.12.25 OpenWrt Lite`** chuyên biệt cho Router OpenWrt (tối ưu RAM nhỏ 128MB–256MB, chạy 13–20 node Passwall2).

---

## 1. Tổng quan Kiến trúc & Nhánh Git

* **Repository**: `https://github.com/mmosonglo/sing-box-v-mmo.git`
* **Nhánh hoạt động (Active Branch)**: `v1.12-lite`
* **Phiên bản nền**: `sing-box 1.12.25` (Go 1.23+)
* **Đặc tính cốt lõi**:
  - **Trạm gác Cất cánh Tuần tự (`AcquireStartupGate`)**: Khóa POSIX `flock` tại `/tmp/sing-box-startup.lock` + Dự báo RAM qua `/proc/*/statm`, giải quyết triệt để sự cố "BÙM 1 nhát full RAM" khi Passwall2 khởi động đồng loạt 13–20 node.
  - **Dọn rác Bộ nhớ Thông minh (`reclaimer`)**: Dọn dẹp One-Shot sau khi khởi tạo xong cấu hình và khi Deep Idle (45s nhàn rỗi), tự động phối hợp hài hòa với ZRAM Swap.
  - **Đường mạng Nguyên bản**: `route/conn.go` giữ nguyên 100% mã nguồn gốc sing-box, **không kiểm soát hay bóp kết nối** (No PreDial Throttling).
  - **Hoàn toàn Im lặng (Silent Mode)**: Không ghi log rác ra hệ thống syslog OpenWrt.
  - **Kích thước siêu nhẹ**: Binary chỉ ~14.6MB (ARM64).

---

## 2. Hướng dẫn Biên dịch & Kiểm thử

```bash
export PATH=$PATH:/home/mmo/.local/go/bin
go test -v -race ./common/butler/...
make openwrt_all
```

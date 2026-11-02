# Installer V2B-ZIC (Debian 11/12, Ubuntu 22.04/24.04, amd64)

Cài đặt (chạy bằng root; tải về file rồi chạy để installer tự cài lệnh `v2b-zic-ctl`):

```bash
curl -fsSL https://raw.githubusercontent.com/Luanhoangkaki/v2b-zic/main/installer/install.sh -o v2b-zic-install.sh && sudo bash v2b-zic-install.sh
```

Installer hỏi: URL Panel (HTTPS), API Key (hiển thị khi nhập), Node ID, loại node (VLESS, Trojan, Shadowsocks, Hysteria2) và chứng chỉ TLS khi cần.

## An toàn
- Binary chỉ tải từ `github.com/Luanhoangkaki/v2b-zic/releases`, bắt buộc khớp `SHA256SUMS`; phiên bản mặc định còn phải khớp mã băm ghim trong script.
- API Key chỉ nằm trong `/etc/v2b-zic/config.json` (root, 0600); không đưa vào tham số dòng lệnh, biến môi trường hay log.
- Kiểm tra HTTPS tới Panel không gửi API Key và không theo redirect.
- Dịch vụ systemd có sandbox (ProtectSystem=strict, NoNewPrivileges...) và `GOMEMLIMIT` bằng 60% RAM cho VPS nhỏ. Thống kê traffic giữ nguyên như V2bX gốc.
- Không đụng V2bX gốc (`/etc/V2bX`, `V2bX.service`).

## Quản lý
`sudo v2b-zic-ctl status | restart | logs [n] | backup | rollback [id] | update [phiên bản] | reconfigure | uninstall [--purge]`

Mỗi lần cài đè, cập nhật hoặc đổi cấu hình đều backup trước (giữ 10 bản mới nhất, trong `/var/backups/v2b-zic`, chứa API Key) và tự rollback nếu node không đồng bộ được user với Panel.

## Giới hạn
- Một node cho mỗi VPS; chứng chỉ qua DNS-01 chưa được hỗ trợ (dùng file có sẵn hoặc HTTP-01 qua cổng 80).
- Hysteria2 tải geoip/geosite lúc chạy từ jsdelivr vào `/var/lib/v2b-zic`.
- Chưa kiểm thử trên Debian/Ubuntu/systemd thật; xem báo cáo kiểm thử khi phát hành.

#!/usr/bin/env bash
# V2B-ZIC installer / manager.
#
# Cài: curl -fsSL <URL install.sh> -o v2b-zic-install.sh && sudo bash v2b-zic-install.sh
# Sau khi cài, quản lý bằng: sudo v2b-zic-ctl {status|restart|logs|backup|rollback|update|reconfigure|uninstall}
#
# Hỗ trợ: Debian 11/12, Ubuntu 22.04/24.04, amd64, systemd.
# Binary chỉ lấy từ GitHub Release của repository, bắt buộc khớp SHA256SUMS và (với
# phiên bản mặc định) khớp mã băm được ghim ngay trong script này.
# API Key chỉ được ghi vào file cấu hình (root, 0600): không đưa vào argv, biến môi
# trường, log hay bản in màn hình sau khi nhập.

set -Eeuo pipefail
umask 077

readonly REPO="Luanhoangkaki/v2b-zic"
readonly PINNED_VERSION="v1.0.0-beta.1"
readonly PINNED_SHA256_AMD64="6b6652f5e1bebd6b0fa86aef07e1ce534e5ba6b2669fa7bf4bbab1859ea8cec9"
readonly SERVICE="v2b-zic"

# Các biến V2B_ZIC_TEST/V2B_ZIC_ROOT/V2B_ZIC_TEST_BASE chỉ dành cho kiểm thử tự động.
P=""
if [ "${V2B_ZIC_TEST:-}" = "1" ]; then P="${V2B_ZIC_ROOT:-}"; fi
readonly P
readonly BIN_ABS="/usr/local/bin/v2b-zic"
readonly CTL_ABS="/usr/local/sbin/v2b-zic-ctl"
readonly CONF_DIR_ABS="/etc/v2b-zic"
readonly DATA_DIR_ABS="/var/lib/v2b-zic"
readonly UNIT_ABS="/etc/systemd/system/${SERVICE}.service"
BIN="$P$BIN_ABS"; CTL="$P$CTL_ABS"; CONF_DIR="$P$CONF_DIR_ABS"; CONF="$CONF_DIR/config.json"
CERT_DIR="$CONF_DIR/cert"; DATA_DIR="$P$DATA_DIR_ABS"; UNIT="$P$UNIT_ABS"
BACKUP_DIR="$P/var/backups/v2b-zic"
readonly BIN CTL CONF_DIR CONF CERT_DIR DATA_DIR UNIT BACKUP_DIR

TMP=""
cleanup() { if [ -n "$TMP" ] && [ -d "$TMP" ]; then rm -rf -- "$TMP"; fi; }
trap cleanup EXIT

msg()  { printf '%s\n' "$*" >&2; }
warn() { printf 'CẢNH BÁO: %s\n' "$*" >&2; }
die()  { printf 'LỖI: %s\n' "$*" >&2; exit 1; }

# Nhập liệu: đọc từ terminal ngay cả khi script được cấp qua pipe.
exec 3<&0
if [ ! -t 0 ] && [ -z "${V2B_ZIC_STDIN:-}" ] && { : </dev/tty; } 2>/dev/null; then exec 3</dev/tty; fi

ask() { # ask VAR "câu hỏi" [mặc định]
  local __var=$1 __q=$2 __def=${3-} __ans
  if [ -n "$__def" ]; then printf '%s [%s]: ' "$__q" "$__def" >&2; else printf '%s: ' "$__q" >&2; fi
  IFS= read -r __ans <&3 || die "Không đọc được dữ liệu nhập."
  __ans=${__ans//$'\r'/}
  __ans=${__ans#"${__ans%%[![:space:]]*}"}
  __ans=${__ans%"${__ans##*[![:space:]]}"}
  if [ -z "$__ans" ]; then __ans=$__def; fi
  printf -v "$__var" '%s' "$__ans"
}

confirm() { # confirm "câu hỏi" -> 0 nếu trả lời y/yes
  local a; ask a "$1 (y/N)" "N"
  case "$a" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

mkd() { local m=$1; shift; mkdir -p -- "$@"; chmod "$m" "$@"; }

# ---------- kiểm tra đầu vào ----------
valid_url() { [[ $1 =~ ^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$ ]]; }
valid_key() { [[ $1 =~ ^[A-Za-z0-9._~+=/-]+$ ]] && [ "${#1}" -ge 8 ] && [ "${#1}" -le 256 ]; }
valid_id()  { [[ $1 =~ ^[1-9][0-9]{0,8}$ ]]; }
valid_path() { [[ $1 =~ ^/[A-Za-z0-9._/-]+$ && $1 != *..* ]]; }
valid_domain() { [[ $1 =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)+$ ]]; }
valid_email() { [[ $1 =~ ^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$ ]]; }
valid_version() { [[ $1 =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.]+)?$ ]]; }

# ---------- tiền kiểm ----------
preflight() {
  if [ "${V2B_ZIC_TEST:-}" != "1" ]; then
    [ "$(id -u)" -eq 0 ] || die "Cần chạy bằng root (sudo)."
    [ "$(uname -s)" = "Linux" ] || die "Chỉ hỗ trợ Linux."
    case "$(uname -m)" in x86_64|amd64) ;; *) die "Chỉ hỗ trợ amd64 (x86_64)." ;; esac
    local os
    os=$( . /etc/os-release 2>/dev/null && printf '%s:%s' "${ID:-}" "${VERSION_ID:-}" ) || os=""
    case "$os" in
      debian:11|debian:12|ubuntu:22.04|ubuntu:24.04) ;;
      *) die "Hệ điều hành không được hỗ trợ (${os:-không rõ}). Hỗ trợ: Debian 11/12, Ubuntu 22.04/24.04." ;;
    esac
    [ -d /run/systemd/system ] || die "Không phát hiện systemd đang chạy."
    if ! command -v curl >/dev/null 2>&1 || [ ! -d /etc/ssl/certs ]; then
      command -v apt-get >/dev/null 2>&1 || die "Thiếu curl và ca-certificates."
      msg "Cài curl và ca-certificates..."
      DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends curl ca-certificates >/dev/null
    fi
  fi
  local c
  for c in curl sha256sum awk install mktemp; do command -v "$c" >/dev/null 2>&1 || die "Thiếu lệnh: $c"; done
}

# ---------- thu thập cấu hình ----------
collect_config() {
  local t
  while :; do
    ask C_URL "URL Panel (HTTPS, ví dụ https://panel.example.com)"
    C_URL=${C_URL%/}
    if valid_url "$C_URL"; then break; fi
    warn "URL không hợp lệ: cần https://, không chứa tài khoản, tham số hay ký tự lạ."
  done
  while :; do
    ask C_KEY "API Key của Panel (sẽ hiển thị khi nhập)"
    if valid_key "$C_KEY"; then break; fi
    warn "API Key không hợp lệ: 8-256 ký tự trong tập A-Z a-z 0-9 . _ ~ + = / -"
  done
  while :; do
    ask C_ID "Node ID"
    if valid_id "$C_ID"; then break; fi
    warn "Node ID phải là số nguyên dương."
  done
  msg "Loại node:  1) VLESS   2) Trojan   3) Shadowsocks   4) Hysteria2"
  while :; do
    ask t "Chọn (1-4)"
    case "$t" in
      1) C_TYPE=vless; C_CORE=xray; break ;;
      2) C_TYPE=trojan; C_CORE=xray; break ;;
      3) C_TYPE=shadowsocks; C_CORE=xray; break ;;
      4) C_TYPE=hysteria2; C_CORE=hysteria2; break ;;
      *) warn "Lựa chọn không hợp lệ." ;;
    esac
  done
  collect_cert
}

collect_cert() {
  local def m
  C_CERTMODE=none; C_CERTFILE=""; C_KEYFILE=""; C_DOMAIN=""; C_EMAIL=""
  case "$C_TYPE" in
    vless) def=1; msg "Chứng chỉ TLS: không cần nếu node dùng REALITY (do Panel cấu hình)." ;;
    shadowsocks) def=1; msg "Chứng chỉ TLS: Shadowsocks thường không cần." ;;
    trojan) def=2; msg "Chứng chỉ TLS: Trojan cần chứng chỉ nếu Panel đặt security là TLS." ;;
    hysteria2) def=2; msg "Chứng chỉ TLS: Hysteria2 bắt buộc có chứng chỉ." ;;
  esac
  msg "  1) Không dùng chứng chỉ cục bộ   2) Dùng file chứng chỉ có sẵn   3) Tự xin chứng chỉ (HTTP-01, cần cổng 80 mở)"
  while :; do
    ask m "Chọn (1-3)" "$def"
    case "$m" in
      1) if [ "$C_TYPE" = hysteria2 ]; then warn "Hysteria2 không chạy được nếu thiếu chứng chỉ."; continue; fi
         C_CERTMODE=none; return ;;
      2) C_CERTMODE=file
         while :; do ask C_CERTFILE "Đường dẫn file chứng chỉ (fullchain, tuyệt đối)"; valid_path "$C_CERTFILE" && break; warn "Đường dẫn không hợp lệ."; done
         while :; do ask C_KEYFILE "Đường dẫn file khóa riêng (tuyệt đối)"; valid_path "$C_KEYFILE" && break; warn "Đường dẫn không hợp lệ."; done
         [ -r "$P$C_CERTFILE" ] || warn "Chưa đọc được $C_CERTFILE lúc này; node sẽ lỗi nếu file không tồn tại khi khởi động."
         [ -r "$P$C_KEYFILE" ] || warn "Chưa đọc được $C_KEYFILE lúc này."
         return ;;
      3) C_CERTMODE=http
         while :; do ask C_DOMAIN "Tên miền của node (đã trỏ về VPS này)"; valid_domain "$C_DOMAIN" && break; warn "Tên miền không hợp lệ."; done
         while :; do ask C_EMAIL "Email đăng ký chứng chỉ"; valid_email "$C_EMAIL" && break; warn "Email không hợp lệ."; done
         C_CERTFILE="$CONF_DIR_ABS/cert/$C_DOMAIN.crt"; C_KEYFILE="$CONF_DIR_ABS/cert/$C_DOMAIN.key"
         return ;;
      *) warn "Lựa chọn không hợp lệ." ;;
    esac
  done
}

check_panel_reachable() {
  # Chỉ kiểm tra TLS/kết nối tới URL gốc; KHÔNG gửi API Key và không theo redirect.
  local code
  if code=$(curl --proto '=https' --tlsv1.2 -sS -o /dev/null -w '%{http_code}' --connect-timeout 10 --max-time 20 "$C_URL" 2>/dev/null); then
    case "$code" in
      3*) warn "Panel trả chuyển hướng (HTTP $code). V2B-ZIC không theo redirect: hãy dùng URL cuối cùng." ;;
      000) warn "Không nhận được phản hồi HTTP từ Panel." ;;
      *) msg "Kết nối HTTPS tới Panel hợp lệ (HTTP $code)." ;;
    esac
  else
    warn "Không kết nối được HTTPS tới Panel (DNS/TLS/mạng). Kiểm tra lại URL trước khi tiếp tục."
    confirm "Vẫn tiếp tục cài đặt?" || die "Đã hủy."
  fi
}

# ---------- tạo cấu hình ----------
write_config() { # write_config <đường dẫn>
  local out=$1 core cert
  if [ "$C_CORE" = hysteria2 ]; then
    core='{
      "Type": "hysteria2",
      "Log": { "Level": "error" }
    }'
  else
    core='{
      "Type": "xray",
      "Log": { "Level": "warning", "AccessPath": "", "ErrorPath": "" },
      "AssetPath": "'"$DATA_DIR_ABS"'/",
      "DnsConfigPath": "",
      "RouteConfigPath": "",
      "InboundConfigPath": "",
      "OutboundConfigPath": ""
    }'
  fi
  case "$C_CERTMODE" in
    none) cert='{ "CertMode": "none" }' ;;
    file) cert='{ "CertMode": "file", "CertFile": "'"$C_CERTFILE"'", "KeyFile": "'"$C_KEYFILE"'" }' ;;
    http) cert='{ "CertMode": "http", "RejectUnknownSni": false, "CertDomain": "'"$C_DOMAIN"'", "CertFile": "'"$C_CERTFILE"'", "KeyFile": "'"$C_KEYFILE"'", "Email": "'"$C_EMAIL"'" }' ;;
    *) die "CertMode nội bộ không hợp lệ." ;;
  esac
  cat >"$out" <<EOF
{
  "Log": { "Level": "info", "Output": "" },
  "Cores": [
    $core
  ],
  "Nodes": [
    {
      "Core": "$C_CORE",
      "ApiHost": "$C_URL",
      "ApiKey": "$C_KEY",
      "NodeID": $C_ID,
      "NodeType": "$C_TYPE",
      "Timeout": 30,
      "ListenIP": "0.0.0.0",
      "SendIP": "0.0.0.0",
      "DeviceOnlineMinTraffic": 200,
      "CertConfig": $cert
    }
  ]
}
EOF
}

install_config() {
  mkd 0750 "$CONF_DIR"
  mkd 0700 "$CERT_DIR"
  local tmp="$CONF_DIR/.config.json.new"
  ( umask 077; write_config "$tmp" )
  chmod 0600 "$tmp"
  mv -f -- "$tmp" "$CONF"
}

# ---------- unit systemd ----------
write_unit() {
  local mem_kb mem_mib limit gogc
  mem_kb=$(awk '/^MemTotal:/{print $2}' /proc/meminfo 2>/dev/null || true)
  [[ ${mem_kb:-} =~ ^[0-9]+$ ]] || mem_kb=1048576
  mem_mib=$(( mem_kb / 1024 ))
  limit=$(( mem_mib * 60 / 100 )); [ "$limit" -ge 128 ] || limit=128
  if [ "$mem_mib" -le 1536 ]; then gogc=75; else gogc=100; fi
  mkd 0750 "$DATA_DIR"
  mkd 0755 "$(dirname "$UNIT")"
  cat >"$UNIT" <<EOF
[Unit]
Description=V2B-ZIC node
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=$DATA_DIR_ABS
ExecStart=$BIN_ABS server --config $CONF_DIR_ABS/config.json --watch=false
Restart=always
RestartSec=10
LimitNOFILE=1048576
# Dịch vụ thoát với mã 0 khi lỗi khởi động (log rồi return), nên cần Restart=always để tự phục hồi.
# Giới hạn mềm bộ nhớ Go (60% RAM) để phù hợp VPS nhỏ; không ảnh hưởng thống kê traffic.
Environment=GOMEMLIMIT=${limit}MiB
Environment=GOGC=$gogc
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$DATA_DIR_ABS $CONF_DIR_ABS/cert
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
LockPersonality=true
RestrictSUIDSGID=true

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 "$UNIT"
}

# ---------- tải và xác minh binary ----------
fetch() { # fetch <tên asset> <đích> <version>
  local name=$1 dest=$2 ver=$3
  if [ "${V2B_ZIC_TEST:-}" = "1" ] && [ -n "${V2B_ZIC_TEST_BASE:-}" ]; then
    cp -- "$V2B_ZIC_TEST_BASE/$name" "$dest"
  else
    curl --proto '=https' --tlsv1.2 -fsSL --retry 3 --connect-timeout 15 --max-time 900 \
      -o "$dest" "https://github.com/$REPO/releases/download/$ver/$name"
  fi
}

download_binary() { # download_binary <version> -> đặt $NEWBIN
  local ver=$1 asset sums expected actual
  valid_version "$ver" || die "Phiên bản không hợp lệ: $ver"
  asset="V2B-ZIC-${ver}-linux-amd64"
  TMP=$(mktemp -d)
  msg "Tải $asset từ GitHub Release $ver..."
  fetch "$asset" "$TMP/$asset" "$ver" || die "Không tải được binary (Release/asset không tồn tại hoặc lỗi mạng)."
  fetch "SHA256SUMS" "$TMP/SHA256SUMS" "$ver" || die "Không tải được SHA256SUMS."
  expected=$(awk -v a="$asset" '($2==a || $2=="*" a){print $1; exit}' "$TMP/SHA256SUMS")
  [[ ${expected:-} =~ ^[0-9a-f]{64}$ ]] || die "SHA256SUMS không có mục hợp lệ cho $asset."
  actual=$(sha256sum "$TMP/$asset" | awk '{print $1}')
  [ "$actual" = "$expected" ] || die "SHA256 không khớp SHA256SUMS. Dừng cài đặt."
  if [ "$ver" = "$PINNED_VERSION" ]; then
    [ "$actual" = "$PINNED_SHA256_AMD64" ] || die "SHA256 không khớp mã băm được ghim trong installer. Dừng cài đặt."
    msg "Đã xác minh SHA256SUMS và mã băm ghim: $actual"
  else
    warn "Phiên bản $ver không có mã băm ghim trong installer; chỉ xác minh bằng SHA256SUMS của Release."
    confirm "Tiếp tục với phiên bản này?" || die "Đã hủy."
  fi
  chmod 0755 "$TMP/$asset"
  if [ "${V2B_ZIC_TEST:-}" != "1" ]; then
    timeout 15 "$TMP/$asset" version >/dev/null 2>&1 || die "Binary không chạy được trên hệ thống này."
  fi
  NEWBIN="$TMP/$asset"
}

put_binary() { mkd 0755 "$(dirname "$BIN")"; install -m 0755 "$NEWBIN" "$BIN.new"; mv -f -- "$BIN.new" "$BIN"; }

put_ctl() {
  local self="${BASH_SOURCE[0]}"
  [ -f "$self" ] || die "Hãy tải install.sh về file rồi chạy (không chạy qua pipe) để cài lệnh v2b-zic-ctl."
  if [ "$(readlink -f "$self" 2>/dev/null || echo "$self")" != "$(readlink -f "$CTL" 2>/dev/null || echo "$CTL")" ]; then
    mkd 0755 "$(dirname "$CTL")"
    install -m 0750 "$self" "$CTL.new"; mv -f -- "$CTL.new" "$CTL"
  fi
}

# ---------- backup / rollback ----------
make_backup() { # make_backup <lý do> -> in id
  local reason=$1 id d f
  id="$(date +%Y%m%d-%H%M%S)-$reason"
  d="$BACKUP_DIR/$id"
  mkd 0700 "$BACKUP_DIR" "$d"
  for f in "$BIN:bin" "$CONF:config.json" "$UNIT:unit"; do
    if [ -f "${f%%:*}" ]; then cp -p -- "${f%%:*}" "$d/${f##*:}"; fi
  done
  if [ -f "$DATA_DIR/installed-version" ]; then cp -p -- "$DATA_DIR/installed-version" "$d/version"; fi
  chmod -R go-rwx "$d"
  prune_backups
  printf '%s\n' "$id"
}

prune_backups() {
  local n=0 d
  while IFS= read -r d; do
    n=$((n+1))
    if [ "$n" -gt 10 ]; then rm -rf -- "$BACKUP_DIR/$d"; fi
  done < <(ls -1 "$BACKUP_DIR" 2>/dev/null | sort -r)
}

restore_backup() { # restore_backup <id>
  local id=$1 d
  [[ $id =~ ^[0-9]{8}-[0-9]{6}-[a-z-]+$ ]] || die "ID backup không hợp lệ."
  d="$BACKUP_DIR/$id"
  [ -d "$d" ] || die "Không tìm thấy backup: $id"
  systemctl stop "$SERVICE" 2>/dev/null || true
  mkd 0750 "$DATA_DIR"
  if [ -f "$d/bin" ]; then install -m 0755 "$d/bin" "$BIN.new"; mv -f -- "$BIN.new" "$BIN"; fi
  if [ -f "$d/config.json" ]; then mkd 0750 "$CONF_DIR"; install -m 0600 "$d/config.json" "$CONF.new"; mv -f -- "$CONF.new" "$CONF"; fi
  if [ -f "$d/unit" ]; then install -m 0644 "$d/unit" "$UNIT"; fi
  if [ -f "$d/version" ]; then install -m 0644 "$d/version" "$DATA_DIR/installed-version"; fi
  systemctl daemon-reload
}

# ---------- khởi động và kiểm tra sức khỏe ----------
# is-active chưa đủ: yêu cầu log xác nhận node đã đồng bộ user từ Panel.
health_check() { # health_check -> 0 nếu node chạy và đã đồng bộ user
  local start i out
  start=$(date +%s)
  systemctl restart "$SERVICE" || return 1
  for i in $(seq 1 45); do
    sleep 1
    systemctl is-active --quiet "$SERVICE" || { sleep 1; systemctl is-active --quiet "$SERVICE" || return 1; }
    out=$(journalctl -u "$SERVICE" --since "@$start" --no-pager -o cat 2>/dev/null || true)
    if grep -q "not have any user" <<<"$out"; then warn "Panel chưa có user nào cho node này. Cấu hình đã đúng; dịch vụ tự thử lại mỗi 10 giây và sẽ chạy khi Panel có user."; return 0; fi
    if grep -Eq 'Load config file failed|new core failed|Start core failed|Run nodes failed' <<<"$out"; then return 1; fi
    if grep -Eq 'Added [0-9]+ new users' <<<"$out"; then return 0; fi
  done
  return 1
}

# ---------- lệnh ----------
current_field() { # in giá trị một trường cấu hình (không bao giờ in ApiKey)
  [ -f "$CONF" ] || return 0
  sed -n 's/^[[:space:]]*"'"$1"'":[[:space:]]*"\{0,1\}\([^",]*\)"\{0,1\},\{0,1\}[[:space:]]*$/\1/p' "$CONF" | head -n1
}

cmd_install() {
  local old=0 bid=""
  preflight
  if [ -f "$CONF" ] || [ -f "$BIN" ]; then
    old=1
    warn "Phát hiện cài đặt V2B-ZIC hiện có. Tiếp tục sẽ ghi đè cấu hình và binary (có backup tự động)."
    confirm "Tiếp tục?" || die "Đã hủy."
  fi
  if [ -d "$P/etc/V2bX" ] || [ -f "$P/etc/systemd/system/V2bX.service" ]; then
    warn "Phát hiện V2bX gốc trên máy. V2B-ZIC dùng đường dẫn/dịch vụ riêng, không đụng V2bX, nhưng hai dịch vụ có thể tranh cổng."
  fi
  collect_config
  check_panel_reachable
  download_binary "${V2B_ZIC_VERSION:-$PINNED_VERSION}"
  if [ "$old" -eq 1 ]; then bid=$(make_backup pre-install); msg "Đã backup: $bid"; fi
  put_binary
  put_ctl
  install_config
  write_unit
  printf '%s\n' "${V2B_ZIC_VERSION:-$PINNED_VERSION}" >"$DATA_DIR/installed-version"
  systemctl daemon-reload
  systemctl enable "$SERVICE" >/dev/null 2>&1
  msg "Khởi động dịch vụ và kiểm tra đồng bộ với Panel (tối đa 45 giây)..."
  if health_check; then
    msg "HOÀN TẤT: dịch vụ đã cài và đang chạy. Quản lý: sudo v2b-zic-ctl status"
  elif [ -n "$bid" ]; then
    warn "Node không khởi động đúng. Tự động rollback về $bid."
    restore_backup "$bid"; health_check || warn "Phiên bản cũ cũng chưa khỏe; xem: sudo v2b-zic-ctl logs"
    exit 1
  else
    systemctl disable --now "$SERVICE" >/dev/null 2>&1 || true
    die "Node không khởi động đúng (cấu hình giữ nguyên, dịch vụ đã tắt). Xem log: journalctl -u $SERVICE -n 50 --no-pager. Sửa bằng: sudo v2b-zic-ctl reconfigure"
  fi
}

cmd_status() {
  [ -f "$BIN" ] || die "V2B-ZIC chưa được cài."
  msg "Phiên bản: $(cat "$DATA_DIR/installed-version" 2>/dev/null || echo không rõ)"
  msg "Panel: $(current_field ApiHost)   Node ID: $(current_field NodeID)   Loại: $(current_field NodeType)"
  msg "Kích hoạt: $(systemctl is-active "$SERVICE" 2>/dev/null || true)   Tự khởi động: $(systemctl is-enabled "$SERVICE" 2>/dev/null || true)"
  msg "Bộ nhớ: $(systemctl show -p MemoryCurrent --value "$SERVICE" 2>/dev/null | awk '{ if ($1 ~ /^[0-9]+$/) printf "%.1f MiB", $1/1048576; else print "n/a" }')"
  journalctl -u "$SERVICE" -n 5 --no-pager -o cat 2>/dev/null | sed -E 's/(token=)[^&" ]+/\1[redacted]/g' >&2 || true
}

cmd_restart() { health_check && msg "Node đang chạy." || die "Node chưa khỏe sau khi restart. Xem: sudo v2b-zic-ctl logs"; }
cmd_logs() { journalctl -u "$SERVICE" -n "${1:-100}" --no-pager -o cat | sed -E 's/(token=)[^&" ]+/\1[redacted]/g'; }
cmd_backup() { local id; id=$(make_backup manual); msg "Đã backup: $BACKUP_DIR/$id (chứa API Key, giữ bí mật)."; }

cmd_rollback() {
  local id=${1:-} cur
  if [ -z "$id" ]; then id=$(ls -1 "$BACKUP_DIR" 2>/dev/null | sort -r | head -n1); fi
  [ -n "$id" ] || die "Chưa có backup."
  msg "Backup khả dụng:"; ls -1 "$BACKUP_DIR" | sort -r | head -n10 >&2
  confirm "Rollback về $id?" || die "Đã hủy."
  cur=$(make_backup pre-rollback); msg "Đã lưu trạng thái hiện tại: $cur"
  restore_backup "$id"
  health_check && msg "Rollback hoàn tất, node đang chạy." || die "Sau rollback node chưa khỏe. Trạng thái trước đó: $cur"
}

cmd_update() {
  local ver=${1:-$PINNED_VERSION} bid
  preflight
  [ -f "$BIN" ] || die "V2B-ZIC chưa được cài."
  download_binary "$ver"
  bid=$(make_backup pre-update); msg "Đã backup: $bid"
  put_binary
  printf '%s\n' "$ver" >"$DATA_DIR/installed-version"
  if health_check; then msg "Cập nhật lên $ver hoàn tất."; else
    warn "Phiên bản mới không khỏe. Tự động rollback về $bid."; restore_backup "$bid"; health_check || true; exit 1
  fi
}

cmd_reconfigure() {
  local bid
  preflight
  [ -f "$BIN" ] || die "V2B-ZIC chưa được cài."
  collect_config
  check_panel_reachable
  bid=$(make_backup pre-reconfigure); msg "Đã backup: $bid"
  install_config
  if health_check; then msg "Cấu hình mới đang chạy."; else
    warn "Cấu hình mới không khỏe. Tự động rollback về $bid."; restore_backup "$bid"; health_check || true; exit 1
  fi
}

cmd_uninstall() {
  local purge=0 a
  [ "${1:-}" = "--purge" ] && purge=1
  [ "${V2B_ZIC_TEST:-}" = "1" ] || [ "$(id -u)" -eq 0 ] || die "Cần chạy bằng root (sudo)."
  warn "Sẽ gỡ dịch vụ và binary V2B-ZIC."
  ask a "Gõ 'yes' để xác nhận"
  [ "$a" = "yes" ] || die "Đã hủy."
  if [ "$purge" -eq 0 ]; then make_backup pre-uninstall >/dev/null; msg "Đã backup cấu hình vào $BACKUP_DIR."; fi
  systemctl disable --now "$SERVICE" >/dev/null 2>&1 || true
  rm -f -- "$UNIT" "$BIN"
  systemctl daemon-reload
  if [ "$purge" -eq 1 ]; then
    rm -rf -- "$CONF_DIR" "$DATA_DIR" "$BACKUP_DIR"
    msg "Đã xóa cấu hình, dữ liệu và backup."
  else
    msg "Giữ nguyên $CONF_DIR và $BACKUP_DIR. Xóa hẳn bằng: rm -rf $CONF_DIR_ABS $DATA_DIR_ABS /var/backups/v2b-zic"
  fi
  rm -f -- "$CTL"
  msg "Đã gỡ V2B-ZIC."
}

usage() {
  cat >&2 <<EOF
V2B-ZIC installer/manager
  install            Cài đặt tương tác (mặc định khi chưa cài)
  status             Trạng thái dịch vụ
  restart            Khởi động lại và kiểm tra đồng bộ Panel
  logs [số dòng]     Xem log gần đây
  backup             Tạo backup binary + cấu hình + unit
  rollback [id]      Khôi phục backup (mặc định mới nhất)
  update [phiên bản] Cập nhật binary từ GitHub Release (giữ cấu hình)
  reconfigure        Nhập lại thông tin Panel/node
  uninstall [--purge] Gỡ cài đặt
EOF
}

main() {
  local cmd=${1:-}
  if [ -z "$cmd" ]; then
    if [ -f "$BIN" ]; then usage; exit 0; else cmd=install; fi
  fi
  shift || true
  case "$cmd" in
    install) cmd_install ;;
    status) cmd_status ;;
    restart) cmd_restart ;;
    logs) cmd_logs "$@" ;;
    backup) cmd_backup ;;
    rollback) cmd_rollback "$@" ;;
    update) cmd_update "$@" ;;
    reconfigure) cmd_reconfigure ;;
    uninstall) cmd_uninstall "$@" ;;
    help|-h|--help) usage ;;
    *) usage; exit 2 ;;
  esac
}

if [ -z "${V2B_ZIC_SOURCE_ONLY:-}" ]; then main "$@"; fi

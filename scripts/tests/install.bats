#!/usr/bin/env bats
# White-box тесты install.sh: sandbox FT_PREFIX/FT_WG_DIR, без root, wg застаблен.

setup() {
    SCRIPT="$BATS_TEST_DIRNAME/../install.sh"
    export FT_PREFIX="$BATS_TEST_TMPDIR/prefix"
    export FT_WG_DIR="$BATS_TEST_TMPDIR/wg"
    mkdir -p "$FT_PREFIX" "$FT_WG_DIR"
    # shellcheck disable=SC1090
    source "$SCRIPT"
    set +e
    trap - EXIT
    PUBLIC_HOST="203.0.113.7"
    wg_up() { return 1; }
    wg() {
        case "$1" in
            genkey) printf 'priv%04d%s=\n' "$RANDOM" "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" | cut -c1-44 ;;
            pubkey) local k; read -r k; printf 'pub-%s\n' "${k:4:39}" ;;
        esac
    }
}

decode_uri() {
    local b=${1#freeturn://}
    b=$(printf '%s' "$b" | tr -- '-_' '+/')
    while [ $(( ${#b} % 4 )) -ne 0 ]; do b="$b="; done
    printf '%s' "$b" | base64 -d
}

need_jq() { command -v jq >/dev/null 2>&1 || skip "нет jq"; }

@test "valid_port" {
    valid_port 1
    valid_port 65535
    ! valid_port 0
    ! valid_port 65536
    ! valid_port 080
    ! valid_port "1 2"
}

@test "valid_hostport: v4, имя, [v6]; без скобок v6 нельзя" {
    valid_hostport 127.0.0.1:51820
    valid_hostport vpn.example.com:443
    valid_hostport '[2001:db8::1]:51820'
    ! valid_hostport '2001:db8::1:51820'
    ! valid_hostport 'a;b:1'
    ! valid_hostport 'host:70000'
    ! valid_hostport 'host'
}

@test "valid_net: сетевой адрес, префикс 16..29" {
    valid_net 10.13.13.0/24
    valid_net 172.20.0.0/16
    ! valid_net 10.13.13.1/24
    ! valid_net 10.13.13.0/30
    ! valid_net 10.13.13.0/8
    ! valid_net 10.13.256.0/24
    ! valid_net 010.13.13.0/24
}

@test "valid_name и valid_version" {
    valid_name owner
    valid_name phone-1.a_b
    ! valid_name ../x
    ! valid_name .hidden
    ! valid_name "a b"
    valid_version latest
    valid_version v3.5.0
    valid_version v3.5.0-rc.1
    ! valid_version 3.5.0
}

@test "valid_kcp: токены argv" {
    valid_kcp ""
    valid_kcp "-kcp-nodelay 1 -kcp-mtu 1200 -kcp-acknodelay=true"
    ! valid_kcp "-kcp-nodelay x"
    ! valid_kcp "-kcp-foo 1"
    ! valid_kcp '-kcp-nodelay 1; rm -rf /'
}

@test "ip2int/int2ip и пересечение подсетей" {
    [ "$(int2ip "$(ip2int 10.13.13.254)")" = 10.13.13.254 ]
    nets_overlap 10.13.13.0/24 10.13.0.0/16
    nets_overlap 10.13.13.0/24 10.13.13.5/32
    ! nets_overlap 10.13.13.0/24 10.13.14.0/24
}

@test "join_hostport: скобки для v6" {
    [ "$(join_hostport 1.2.3.4 56000)" = 1.2.3.4:56000 ]
    [ "$(join_hostport 2001:db8::1 56000)" = '[2001:db8::1]:56000' ]
}

@test "parse_opts: kcp заменяет набор целиком, ошибки -> bad_arg" {
    KCP_ARGS="-kcp-mtu 1000"
    parse_opts --kcp-nodelay=1 --kcp-acknodelay=true
    [ "$KCP_ARGS" = "-kcp-nodelay 1 -kcp-acknodelay=true" ]
    valid_kcp "$KCP_ARGS"
    _IS_RPC=1
    run parse_opts --connect='x;y:1'
    [[ "$output" == *'"code":"bad_arg"'* ]]
    run parse_opts --bogus
    [[ "$output" == *'"code":"bad_arg"'* ]]
}

@test "server_argv: new -> свой WG, obf, clients-file" {
    BACKEND=new WG_PORT=51999 LISTEN_PORT=56001 OBF_PROFILE=rtpopus3 OBF_KEY=abc
    run server_argv
    [ "${lines[0]}" = -listen ] && [ "${lines[1]}" = 0.0.0.0:56001 ]
    [ "${lines[3]}" = 127.0.0.1:51999 ]
    [[ "$output" == *$'-clients-file\n'"$CLIENTSFILE"* ]]
    [[ "$output" == *$'-obf-profile\nrtpopus3\n-obf-key\nabc'* ]]
}

@test "server_argv: external tcp + kcp, obf none" {
    BACKEND=external CONNECT=127.0.0.1:443 PROXY_MODE=tcp OBF_PROFILE=none KCP_ARGS="-kcp-mtu 1200"
    run server_argv
    [ "${lines[3]}" = 127.0.0.1:443 ]
    [[ "$output" != *obf* ]]
    [ "${lines[-2]}" = -kcp-mtu ] && [ "${lines[-1]}" = 1200 ]
    PROXY_MODE=udp
    run server_argv
    [[ "$output" != *kcp* ]]
}

@test "validate_config: tcp со своим WG запрещён, ключ генерится" {
    _IS_RPC=1
    BACKEND=new PROXY_MODE=tcp
    run validate_config
    [[ "$output" == *'"code":"bad_arg"'* ]]
    PROXY_MODE=udp OBF_KEY=""
    validate_config
    valid_hex64 "$OBF_KEY"
    OBF_PROFILE=none
    validate_config
    [ -z "$OBF_KEY" ]
}

@test "validate_config: external без connect -> bad_arg" {
    _IS_RPC=1
    BACKEND=external CONNECT=""
    run validate_config
    [[ "$output" == *'"code":"bad_arg"'* ]]
}

@test "save_config/load_config: круг и отпечаток ссылок" {
    BACKEND=external CONNECT=10.0.0.2:443 PROXY_MODE=tcp KCP_ARGS="-kcp-mtu 1200"
    save_config
    BACKEND=new CONNECT="" KCP_ARGS=""
    load_config
    [ "$BACKEND" = external ] && [ "$CONNECT" = 10.0.0.2:443 ] && [ "$KCP_ARGS" = "-kcp-mtu 1200" ]
    [ "$PREV_SHARE" = "$(share_fingerprint)" ]
    [ "$PREV_LISTEN_PORT" = "$LISTEN_PORT" ]
}

@test "meta_put/meta_get/meta_del: замена на месте" {
    meta_put "a|1|||"
    meta_put "b|2|||"
    meta_put "a|9|||"
    [ "$(head -n1 "$CLIENTS_META")" = "a|9|||" ]
    [ "$(wc -l < "$CLIENTS_META")" -eq 2 ]
    meta_del a
    ! meta_has a
    meta_has b
}

@test "freeturn_uri: схема internal/uri" {
    OBF_PROFILE=rtpopus3 OBF_KEY=$(printf 'a%.0s' {1..64}) PROXY_MODE=tcp LISTEN_PORT=56000
    json=$(decode_uri "$(freeturn_uri cid123 phone $'[Interface]\nA = 1')")
    [[ "$json" == '{"v":1,"provider":"vk","peer":"203.0.113.7:56000","mode":"tcp","obf":"rtpopus3"'* ]]
    [[ "$json" == *'"cid":"cid123","name":"phone","wg":"[Interface]\nA = 1"}' ]]
    OBF_PROFILE=none PROXY_MODE=udp
    json=$(decode_uri "$(freeturn_uri c n "")")
    [[ "$json" != *obf* && "$json" != *mode* && "$json" != *wg* ]]
}

@test "wg: конфиг, выдача адресов, вырез пира" {
    WG_NET=10.13.13.0/24 WG_PORT=51820
    wg_write_conf
    wg_ours
    [ "$(wg_conf_net)" = 10.13.13.0/24 ]
    grep -q '^Address = 10.13.13.1/24$' "$WG_CONF"
    grep -q '^PostUp = iptables -t nat -A POSTROUTING -s 10.13.13.0/24 ! -d 10.13.13.0/24 -j MASQUERADE$' "$WG_CONF"
    mkdir -p "$(client_dir a)" "$(client_dir b)"
    wg_peer_add a
    [ "$PEER_IP" = 10.13.13.2 ]
    wg_peer_add b
    [ "$PEER_IP" = 10.13.13.3 ]
    grep -q '^Endpoint = 127.0.0.1:9000$' "$(client_conf_file b)"
    grep -q '^MTU = 1280$' "$(client_conf_file b)"
    wg_peer_remove a
    ! grep -q 'ft-client: a$' "$WG_CONF"
    grep -q 'ft-client: b$' "$WG_CONF"
    [ "$(wg_next_ip)" = 10.13.13.2 ]
}

@test "wg_next_ip: подсеть кончилась" {
    WG_NET=10.13.13.0/29
    wg_write_conf
    for i in 2 3 4 5 6; do printf '[Peer]\nAllowedIPs = 10.13.13.%s/32\n' "$i" >> "$WG_CONF"; done
    ! wg_next_ip
}

@test "wg_apply: чужой ft-wg0.conf не трогаем" {
    _IS_RPC=1
    has_systemd() { return 0; }
    wg_tools_ensure() { :; }
    printf '[Interface]\nAddress = 10.0.0.1/24\n' > "$WG_CONF"
    run wg_apply
    [[ "$output" == *'"code":"wg_conflict"'* ]]
}

@test "клиенты: external - только client_id, хозяина не удалить" {
    need_jq
    BACKEND=external
    auth_init
    clients_sync
    meta_has owner
    cid=$(meta_get owner | cut -d'|' -f2)
    [[ "$cid" =~ ^[0-9a-f]{32}$ ]]
    [ "$(jq -r --arg id "$cid" '.clients[$id].comment' "$CLIENTSFILE")" = owner ]
    [ -s "$(client_dir owner)/owner.link" ]
    [ ! -f "$(client_conf_file owner)" ]
    client_create phone
    _IS_RPC=1
    run client_create phone
    [[ "$output" == *'"code":"exists"'* ]]
    run client_delete owner
    [[ "$output" == *'"code":"owner_protected"'* ]]
    run client_delete nobody
    [[ "$output" == *'"code":"not_found"'* ]]
    pcid=$(meta_get phone | cut -d'|' -f2)
    client_delete phone
    ! meta_has phone
    [ "$(jq -r --arg id "$pcid" '.clients[$id] // "gone"' "$CLIENTSFILE")" = gone ]
    [ ! -d "$(client_dir phone)" ]
}

@test "клиенты: переход external -> new даёт пиры всем, снос WG их убирает" {
    need_jq
    BACKEND=external
    auth_init
    clients_sync
    client_create phone
    BACKEND=new WG_NET=10.13.13.0/24
    wg_write_conf
    clients_sync
    [ "$(meta_get owner | cut -d'|' -f3)" = 10.13.13.2 ]
    [ "$(meta_get phone | cut -d'|' -f3)" = 10.13.13.3 ]
    [ -f "$(client_conf_file phone)" ]
    json=$(decode_uri "$(cat "$(client_dir phone)/phone.link")")
    [[ "$json" == *'"wg":"[Interface]'* ]]
    clients_drop_peers
    [ -z "$(meta_get phone | cut -d'|' -f3)" ]
    [ ! -f "$(client_conf_file phone)" ]
    json=$(decode_uri "$(cat "$(client_dir phone)/phone.link")")
    [[ "$json" != *'"wg"'* ]]
}

@test "rpc client-list / client-conf: JSON" {
    need_jq
    BACKEND=external CONNECT=127.0.0.1:443 OBF_PROFILE=none
    save_config
    auth_init
    clients_sync
    run bash "$SCRIPT" client-list
    [ "$(jq -r .result <<< "$output")" = ok ]
    [ "$(jq -r '.data.clients[0].name' <<< "$output")" = owner ]
    [ "$(jq -r '.data.clients[0].self' <<< "$output")" = true ]
    [ "$(jq -r '.data.share.host' <<< "$output")" = 203.0.113.7 ]
    [ "$(jq -r '.data.share.backend' <<< "$output")" = external ]
    run bash "$SCRIPT" client-conf --name=owner
    [[ "$(jq -r .data.link <<< "$output")" == freeturn://* ]]
    run bash "$SCRIPT" client-conf --name=ghost
    [ "$(jq -r .code <<< "$output")" = not_found ]
}

@test "rpc probe: без install.conf нет config" {
    run bash "$SCRIPT" probe
    [[ "$output" == '{"proto":3,"result":"ok"'* ]]
    [[ "$output" != *'"config"'* ]]
}

@test "rpc: мутирующие команды без root -> needs_root" {
    [ "$(id -u)" -ne 0 ] || skip "запущено от root"
    for c in apply client-add uninstall stop restart; do
        run bash "$SCRIPT" "$c" --name=x
        [[ "$output" == *'"code":"needs_root"'* ]] || [[ "$output" == *'"code":"not_installed"'* ]]
    done
}

@test "validate_config: web-port и web-ttl" {
    _IS_RPC=1
    BACKEND=external CONNECT=127.0.0.1:443
    FT_WEB_PORT=99999
    run validate_config
    [[ "$output" == *'"code":"bad_arg"'* ]]
    FT_WEB_PORT=8080 FT_WEB_TTL=10
    run validate_config
    [[ "$output" == *'"code":"bad_arg"'* ]]
}

@test "диспетчер: CLI-флаги не RPC, все RPC-команды опознаются" {
    for f in -y --yes --update --uninstall --reconfigure --purge client ""; do
        ! is_rpc_command "$f"
    done
    for c in $RPC_COMMANDS; do
        is_rpc_command "$c"
        declare -F "cmd_${c//-/_}" >/dev/null
    done
}

@test "web_token стабилен, client_token свой у каждого клиента" {
    t1=$(web_token); t2=$(web_token)
    [ "$t1" = "$t2" ] && [ "${#t1}" -eq 32 ] && [ -f "$WEB_TOKEN_FILE" ]
    a=$(client_token phone); b=$(client_token laptop)
    [ "$a" = "$(client_token phone)" ] && [ "$a" != "$b" ] && [ "$a" != "$t1" ] && [ "${#a}" -eq 32 ]
    WEB_TOKEN_FILE=/proc/nonexistent/web.token
    ! web_token
}

@test "_web_layout: каталог клиента только за его токеном, удалённый убирается" {
    mkdir -p "$(client_dir phone)" "$(client_dir laptop)"
    printf 'phone|a|||now\nlaptop|b|||now\n' > "$CLIENTS_META"
    _web_layout
    [ -f "$WEB_ROOT/index.html" ] && [ -f "$WEB_ROOT/$(web_token)$WEB_PROBE_EXT" ]
    [ "$(readlink "$WEB_ROOT/$(client_token phone)")" = "$(client_dir phone)" ]
    [ ! -L "$WEB_ROOT/$(web_token)" ]
    gone=$(client_token laptop)
    printf 'phone|a|||now\n' > "$CLIENTS_META"
    rm -rf "$(client_dir laptop)"
    _web_layout
    [ ! -L "$WEB_ROOT/$gone" ]
}

@test "ensure_web_server: порт открывает сам и закрывает при неудаче" {
    fw="$BATS_TEST_TMPDIR/fw.log"
    firewall_open_port()  { echo "open $1/$2" >> "$fw"; }
    firewall_close_port() { echo "close $1/$2" >> "$fw"; }
    _web_listening() { return 1; }
    _web_systemd_active() { return 1; }
    _web_server_argv() { printf '%s\n' fake-httpd "$1"; }
    _web_start_systemd() { return 1; }
    _web_start_nohup() { return 1; }
    ss() { :; }
    ! ensure_web_server
    grep -q 'open 8080/tcp' "$fw"
    grep -q 'close 8080/tcp' "$fw"
}

@test "_web_server_argv: python3, иначе busybox" {
    HAVE=""
    command() {
        if [ "$1" = -v ]; then
            case " $HAVE " in *" $2 "*) echo "/usr/bin/$2"; return 0 ;; esac
            return 1
        fi
        builtin command "$@"
    }
    pkg_install() { return 1; }
    ! _web_server_argv 8080
    HAVE=busybox
    [ "$(_web_server_argv 8080 | tr '\n' ' ')" = "busybox httpd -f -p 8080 -h $WEB_ROOT " ]
    HAVE="python3 busybox"
    [ "$(_web_server_argv 8080 | tr '\n' ' ')" = "python3 -m http.server 8080 --directory $WEB_ROOT " ]
}

@test "get_public_ip: ошибка вместо плейсхолдера" {
    curl() { return 1; }
    ! get_public_ip
}

@test "client_delete убирает веб-симлинк клиента" {
    need_jq
    BACKEND=external
    auth_init
    clients_sync
    client_create phone
    _web_layout
    tok=$(client_token phone)
    [ -L "$WEB_ROOT/$tok" ]
    client_delete phone
    [ ! -L "$WEB_ROOT/$tok" ]
}

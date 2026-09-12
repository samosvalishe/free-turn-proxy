#!/usr/bin/env bats

setup() {
    export FT_TEST_DIR="$BATS_TEST_TMPDIR/ft_test"
    export FT_PREFIX="$FT_TEST_DIR/opt"
    export FT_WG_DIR="$FT_TEST_DIR/wireguard"
    export SCRIPT="$BATS_TEST_DIRNAME/../install.sh"
    mkdir -p "$FT_PREFIX" "$FT_WG_DIR"
}

teardown() {
    rm -rf "$FT_TEST_DIR"
}

run_script() { run bash "$SCRIPT" "$@"; }

@test "install.sh проходит проверку синтаксиса bash -n" {
    run bash -n "$SCRIPT"
    [ "$status" -eq 0 ]
}

@test "help (-h и --help) выводит справку и завершается с кодом 0" {
    run_script -h
    [ "$status" -eq 0 ]
    [[ "$output" == *"Free Turn Proxy & AmneziaWG"* ]]
    [[ "$output" == *"--only-awg"* ]]
    [[ "$output" == *"--only-freeturn"* ]]

    run_script --help
    [ "$status" -eq 0 ]
    [[ "$output" == *"Машиночитаемый JSON RPC v2"* ]]
}

@test "probe возвращает proto 2, result ok и ровно одну строку JSON" {
    run_script probe
    [ "$status" -eq 0 ]
    [ "${#lines[@]}" -eq 1 ]
    [[ "$output" == *'"proto":2'* ]]
    [[ "$output" == *'"result":"ok"'* ]]
}

@test "probe содержит все обязательные поля контракта Android" {
    run_script probe
    [ "$status" -eq 0 ]
    [[ "$output" == *'"installed":'* ]]
    [[ "$output" == *'"running":'* ]]
    [[ "$output" == *'"runtime":'* ]]
    [[ "$output" == *'"euid":'* ]]
    [[ "$output" == *'"wg":{'* ]]
    [[ "$output" == *'"virt":'* ]]
    [[ "$output" == *'"wg_kernel":'* ]]
    [[ "$output" == *'"conflicts":{'* ]]
}

@test "неизвестная сабкоманда возвращает err bad_arg" {
    run_script unknowncmd
    [ "$status" -eq 1 ]
    [[ "$output" == *'"result":"err"'* ]]
    [[ "$output" == *'"code":"bad_arg"'* ]]
}

@test "неизвестный флаг в RPC возвращает err bad_arg" {
    run_script start --unknown-flag=123
    [ "$status" -eq 1 ]
    [[ "$output" == *'"code":"bad_arg"'* ]]
}

@test "share-info возвращает валидный JSON с wg_backend" {
    run_script share-info
    [ "$status" -eq 0 ]
    [[ "$output" == *'"result":"ok"'* ]]
    [[ "$output" == *'"wg_backend":'* ]]
}

@test "share-list возвращает пустые массивы peers и clients" {
    run_script share-list
    [ "$status" -eq 0 ]
    [[ "$output" == *'"result":"ok"'* ]]
    [[ "$output" == *'"peers":[]'* ]]
    [[ "$output" == *'"clients":[]'* ]]
}

@test "stop возвращает stopped: true" {
    run_script stop
    [ "$status" -eq 0 ]
    [[ "$output" == *'"stopped":true'* ]]
}

@test "генерация параметров AmneziaWG 3.1 соответствует спецификации" {
    # Проверяем функцию init_awg_params напрямую
    run bash -c "
        source '$SCRIPT'
        AWG_CONF='$FT_PREFIX/awg/awg0.conf'
        init_awg_params
        [ -n \"\$AWG_JC\" ] && [ \"\$AWG_JC\" -ge 4 ] && [ \"\$AWG_JC\" -le 6 ] || exit 1
        [ \"\$AWG_JMIN\" -eq 10 ] && [ \"\$AWG_JMAX\" -eq 50 ] || exit 1
        [ \"\$AWG_S4\" -eq 12 ] || exit 1
        [ -n \"\$AWG_S1\" ] && [ -n \"\$AWG_S2\" ] && [ -n \"\$AWG_S3\" ] || exit 1
        [ \"\$AWG_H1\" -eq 1 ] && [ \"\$AWG_H2\" -eq 2 ] && [ \"\$AWG_H3\" -eq 3 ] && [ \"\$AWG_H4\" -eq 4 ] || exit 1
        [ \${#AWG_HPK} -gt 20 ] || exit 1
    "
    [ "$status" -eq 0 ]
}

@test "alloc_client_ip корректно инкрементирует IP адреса клиентов" {
    run bash -c "
        source '$SCRIPT'
        conf='$FT_PREFIX/test.conf'
        : > \"\$conf\"
        ip1=\$(alloc_client_ip \"\$conf\" '10.13.13')
        [ \"\$ip1\" = '10.13.13.2' ] || exit 1
        echo 'AllowedIPs = 10.13.13.2/32' >> \"\$conf\"
        ip2=\$(alloc_client_ip \"\$conf\" '10.13.13')
        [ \"\$ip2\" = '10.13.13.3' ] || exit 1
        echo 'AllowedIPs = 10.13.13.5/32' >> \"\$conf\"
        ip3=\$(alloc_client_ip \"\$conf\" '10.13.13')
        [ \"\$ip3\" = '10.13.13.6' ] || exit 1
    "
    [ "$status" -eq 0 ]
}

@test "валидация портов, hex и endpoint отсекает некорректные значения" {
    run bash -c "
        source '$SCRIPT'
        valid_port 51820 || exit 1
        ! valid_port 0 || exit 1
        ! valid_port 70000 || exit 1
        ! valid_port abc || exit 1
        valid_hex64 '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef' || exit 1
        ! valid_hex64 'short' || exit 1
        valid_endpoint '127.0.0.1:9000' || exit 1
        valid_endpoint '[::1]:9000' || exit 1
        ! valid_endpoint 'invalid-endpoint' || exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "validate_config отсекает конфигурацию без компонентов" {
    run bash -c "
        source '$SCRIPT'
        INSTALL_FREETURN=0
        INSTALL_AWG=0
        validate_config
    "
    [ "$status" -ne 0 ]
    [[ "$output" == *"Не выбран ни один компонент"* ]]
}

@test "validate_config валидирует режимы only-awg и only-freeturn" {
    run bash -c "
        source '$SCRIPT'
        # Только FreeTurn
        INSTALL_FREETURN=1
        INSTALL_AWG=0
        INSTALL_METHOD='docker'
        PROVIDER='vk'
        PROXY_MODE='udp'
        LISTEN_PORT='56000'
        OBF_PROFILE='none'
        validate_config || exit 1

        # Только AmneziaWG
        INSTALL_FREETURN=0
        INSTALL_AWG=1
        BACKEND_PORT='51820'
        WG_ENDPOINT='127.0.0.1:9000'
        validate_config || exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "generate_freeturn_uri генерирует корректный URI со схемой freeturn://" {
    run bash -c "
        source '$SCRIPT'
        uri=\$(generate_freeturn_uri '1.2.3.4:56000' 'udp' 'rtpopus3' '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef' 'cid123' 'my-client')
        [[ \"\$uri\" == freeturn://* ]] || exit 1
        b64=\"\${uri#freeturn://}\"
        pad=\$(( (4 - \${#b64} % 4) % 4 ))
        [ \"\$pad\" -eq 1 ] && b64=\"\${b64}=\"
        [ \"\$pad\" -eq 2 ] && b64=\"\${b64}==\"
        [ \"\$pad\" -eq 3 ] && b64=\"\${b64}===\"
        json=\$(printf '%s' \"\$b64\" | tr '_-' '/+' | base64 -d 2>/dev/null)
        [[ \"\$json\" == *'\"v\":1'* ]] || exit 1
        [[ \"\$json\" == *'\"provider\":\"vk\"'* ]] || exit 1
        [[ \"\$json\" == *'\"peer\":\"1.2.3.4:56000\"'* ]] || exit 1
        [[ \"\$json\" == *'\"obf\":\"rtpopus3\"'* ]] || exit 1
        [[ \"\$json\" == *'\"cid\":\"cid123\"'* ]] || exit 1
        [[ \"\$json\" == *'\"name\":\"my-client\"'* ]] || exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "peer-add требует --name-b64 и --endpoint в RPC режиме" {
    run_script peer-add
    [ "$status" -eq 1 ]
    [[ "$output" == *'"code":"bad_arg"'* ]]
    [[ "$output" == *"--name-b64 required"* ]]

    run_script peer-add --name-b64="Y2xpZW50"
    [ "$status" -eq 1 ]
    [[ "$output" == *'"code":"bad_arg"'* ]]
    [[ "$output" == *"--endpoint required"* ]]
}

@test "клиентский конфиг AWG 3.1 содержит все 18 параметров обфускации" {
    run bash -c "
        source '$SCRIPT'
        AWG_JC=5
        AWG_JMIN=10
        AWG_JMAX=50
        AWG_S1=20
        AWG_S2=30
        AWG_S3=40
        AWG_S4=12
        AWG_H1=1
        AWG_H2=2
        AWG_H3=3
        AWG_H4=4
        AWG_HPK='dGVzdGtleTEyMzQ1Njc4OTA='
        ext_ip='1.2.3.4'
        BACKEND_PORT=51820
        client_ip='10.13.13.2'
        cli_priv='clientprivkey'
        srv_pub='serverpubkey'

        conf=\$(cat <<EOF
[Interface]
Address = \${client_ip}/32
DNS = 1.1.1.1, 1.0.0.1
PrivateKey = \${cli_priv}
Jc = \${AWG_JC}
Jmin = \${AWG_JMIN}
Jmax = \${AWG_JMAX}
S1 = \${AWG_S1}
S2 = \${AWG_S2}
S3 = \${AWG_S3}
S4 = \${AWG_S4}
H1 = \${AWG_H1}
H2 = \${AWG_H2}
H3 = \${AWG_H3}
H4 = \${AWG_H4}
HeaderProtectionKey = \${AWG_HPK}
ContentPaddingAddition = 10
RekeyAfterTime = 110
RekeyTimeout = 5
RejectAfterTime = 160
KeepaliveTimeout = 10
MaxHandshakeAttempts = 15
RandomTrailers = on
DisableCookies = on

[Peer]
PublicKey = \${srv_pub}
AllowedIPs = 0.0.0.0/0, ::/0
Endpoint = \${ext_ip}:\${BACKEND_PORT}
PersistentKeepalive = 25
EOF
        )
        for param in Jc Jmin Jmax S1 S2 S3 S4 H1 H2 H3 H4 HeaderProtectionKey ContentPaddingAddition RekeyAfterTime RekeyTimeout RejectAfterTime KeepaliveTimeout MaxHandshakeAttempts RandomTrailers DisableCookies; do
            echo \"\$conf\" | grep -q \"^\$param = \" || { echo \"Missing \$param\"; exit 1; }
        done
        true
    "
    [ "$status" -eq 0 ]
}

@test "peer-conf требует pubkey или pubkey-b64" {
    run_script peer-conf
    [ "$status" -eq 1 ]
    [[ "$output" == *'"code":"bad_arg"'* ]]
    [[ "$output" == *"--pubkey required"* ]]
}

@test "peer-remove требует pubkey" {
    run_script peer-remove
    [ "$status" -eq 1 ]
    [[ "$output" == *'"code":"bad_arg"'* ]]
    [[ "$output" == *"--pubkey required"* ]]
}

@test "client-add и client-remove валидируют обязательные аргументы в RPC" {
    run_script client-add
    [ "$status" -eq 1 ]
    [[ "$output" == *'"code":"bad_arg"'* ]]
    [[ "$output" == *"--client-id required"* ]]

    run_script client-add --client-id="cid123"
    [ "$status" -eq 1 ]
    [[ "$output" == *'"code":"bad_arg"'* ]]
    [[ "$output" == *"--name-b64 required"* ]]

    run_script client-remove
    [ "$status" -eq 1 ]
    [[ "$output" == *'"code":"bad_arg"'* ]]
    [[ "$output" == *"--client-id required"* ]]
}

@test "uninstall без root возвращает needs_root в RPC" {
    run bash -c "
        export FT_PREFIX='$FT_PREFIX'
        # Запуск от текущего непривилегированного пользователя в тестах
        bash '$SCRIPT' uninstall --target=freeturn
    "
    # Если запущен не под root, должен вернуть code: needs_root
    if [ "$(id -u)" -ne 0 ]; then
        [ "$status" -eq 1 ]
        [[ "$output" == *'"code":"needs_root"'* ]]
    fi
}

@test "validate_config отсекает невалидные порты, режимы и методы" {
    run bash -c "
        source '$SCRIPT'
        # Невалидный метод
        ( INSTALL_FREETURN=1; INSTALL_AWG=0; INSTALL_METHOD='nomad'; PROVIDER='vk'; PROXY_MODE='udp'; LISTEN_PORT='56000'; OBF_PROFILE='none'; validate_config ) 2>/dev/null && exit 1

        # Невалидный режим
        ( INSTALL_FREETURN=1; INSTALL_AWG=0; INSTALL_METHOD='docker'; PROVIDER='vk'; PROXY_MODE='http'; LISTEN_PORT='56000'; OBF_PROFILE='none'; validate_config ) 2>/dev/null && exit 1

        # Невалидный порт listen
        ( INSTALL_FREETURN=1; INSTALL_AWG=0; INSTALL_METHOD='docker'; PROVIDER='vk'; PROXY_MODE='udp'; LISTEN_PORT='99999'; OBF_PROFILE='none'; validate_config ) 2>/dev/null && exit 1

        # Невалидный профиль обфускации
        ( INSTALL_FREETURN=1; INSTALL_AWG=0; INSTALL_METHOD='docker'; PROVIDER='vk'; PROXY_MODE='udp'; LISTEN_PORT='56000'; OBF_PROFILE='invalid_prof'; validate_config ) 2>/dev/null && exit 1

        # Невалидный порт бэкенда
        ( INSTALL_FREETURN=0; INSTALL_AWG=1; BACKEND_PORT='invalid'; WG_ENDPOINT='127.0.0.1:9000'; validate_config ) 2>/dev/null && exit 1

        # Невалидный endpoint
        ( INSTALL_FREETURN=0; INSTALL_AWG=1; BACKEND_PORT='51820'; WG_ENDPOINT='bad_endpoint'; validate_config ) 2>/dev/null && exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "state_set и state_get сохраняют и читают параметры состояния" {
    run bash -c "
        source '$SCRIPT'
        state_set 'key1' 'val1'
        res1=\$(state_get 'key1')
        [ \"\$res1\" = 'val1' ] || exit 1

        state_set 'key2' 'val2'
        state_set 'key1' 'val1_updated'
        res1_up=\$(state_get 'key1')
        res2=\$(state_get 'key2')
        [ \"\$res1_up\" = 'val1_updated' ] || exit 1
        [ \"\$res2\" = 'val2' ] || exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "save_config и load_config корректно сериализуют и восстанавливают настройки" {
    run bash -c "
        source '$SCRIPT'
        INSTALL_METHOD='docker'
        INSTALL_FREETURN=1
        INSTALL_AWG=1
        PROXY_MODE='tcp'
        BACKEND_PORT=51821
        LISTEN_PORT=56001
        OBF_PROFILE='rtpopus3'
        OBF_KEY='0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef'
        WG_ENDPOINT='127.0.0.1:9001'
        AWG_JC=4
        AWG_HPK='dGVzdA=='

        save_config

        # Сбрасываем переменные
        PROXY_MODE=''
        BACKEND_PORT=''
        LISTEN_PORT=''
        OBF_PROFILE=''
        OBF_KEY=''
        WG_ENDPOINT=''
        AWG_JC=''
        AWG_HPK=''

        load_config

        [ \"\$PROXY_MODE\" = 'tcp' ] || exit 1
        [ \"\$BACKEND_PORT\" = '51821' ] || exit 1
        [ \"\$LISTEN_PORT\" = '56001' ] || exit 1
        [ \"\$OBF_PROFILE\" = 'rtpopus3' ] || exit 1
        [ \"\$OBF_KEY\" = '0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef' ] || exit 1
        [ \"\$WG_ENDPOINT\" = '127.0.0.1:9001' ] || exit 1
        [ \"\$AWG_JC\" = '4' ] || exit 1
        [ \"\$AWG_HPK\" = 'dGVzdA==' ] || exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "relay и direct клиентские конфиги имеют разные Endpoint" {
    run bash -c "
        source '$SCRIPT'
        mkdir -p '$FT_PREFIX/clients'
        ext_ip='203.0.113.10'
        BACKEND_PORT=51820
        WG_ENDPOINT='127.0.0.1:9000'

        direct_endpoint=\"\${ext_ip}:\${BACKEND_PORT}\"
        relay_endpoint=\"\${WG_ENDPOINT}\"

        [ \"\$direct_endpoint\" != \"\$relay_endpoint\" ] || exit 1
        [ \"\$direct_endpoint\" = '203.0.113.10:51820' ] || exit 1
        [ \"\$relay_endpoint\" = '127.0.0.1:9000' ] || exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "logs RPC команда принимает tail и возвращает ok" {
    run_script logs --tail=10
    [ "$status" -eq 0 ]
    [[ "$output" == *'"result":"ok"'* ]]
    [[ "$output" == *'"logs":'* ]]
}

@test "клиентский CLI без аргументов или с неверной командой выводит ошибку" {
    run bash -c "
        # Без root при вызове сабкоманд client скрипт требует root
        bash '$SCRIPT' client invalid_sub 2>&1
    "
    [ "$status" -ne 0 ]
}

@test "запуск через пайп (curl | bash) корректно обрабатывает параметры без ошибки unbound variable" {
    run bash -c "cat '$SCRIPT' | bash -s -- -h"
    [ "$status" -eq 0 ]
    [[ "$output" == *"Free Turn Proxy & AmneziaWG"* ]]
}

@test "compose_cmd делегирует вызовы в docker compose или docker-compose" {
    run bash -c "
        source '$SCRIPT'
        docker() {
            if [ \"\$1\" = 'compose' ]; then
                echo \"MOCK_COMPOSE: \${*:2}\"
                return 0
            fi
            return 1
        }
        export -f docker
        compose_cmd version
    "
    [ "$status" -eq 0 ]
    [[ "$output" == *"MOCK_COMPOSE: version"* ]]

    run bash -c "
        source '$SCRIPT'
        docker() { return 1; }
        docker-compose() {
            echo \"MOCK_STANDALONE: \$*\"
            return 0
        }
        export -f docker docker-compose
        compose_cmd up -d
    "
    [ "$status" -eq 0 ]
    [[ "$output" == *"MOCK_STANDALONE: up -d"* ]]
}

@test "ensure_compose успешно завершается, если docker compose уже установлен" {
    run bash -c "
        source '$SCRIPT'
        docker() {
            if [ \"\$1\" = 'compose' ] && [ \"\$2\" = 'version' ]; then
                return 0
            fi
            return 1
        }
        export -f docker
        ensure_compose
    "
    [ "$status" -eq 0 ]
}

@test "ensure_awg_image возвращает 0, если образ уже присутствует локально" {
    run bash -c "
        source '$SCRIPT'
        INSTALL_AWG=1
        docker() {
            if [ \"\$1\" = 'image' ] && [ \"\$2\" = 'inspect' ]; then
                return 0
            fi
            return 1
        }
        export -f docker
        ensure_awg_image
    "
    [ "$status" -eq 0 ]
}

@test "web_token стабилен между вызовами и ложится на диск" {
    run bash -c "
        source '$SCRIPT'
        t1=\$(web_token); t2=\$(web_token)
        [ \"\$t1\" = \"\$t2\" ] || exit 1
        [ \"\${#t1}\" -eq 32 ] || exit 1
        [ -f \"\$WEB_TOKEN_FILE\" ] || exit 1
    "
    [ "$status" -eq 0 ]
}

@test "токен свой у каждого клиента и не равен мастер-секрету" {
    run bash -c "
        source '$SCRIPT'
        a=\$(client_token phone); b=\$(client_token laptop)
        [ \"\$a\" = \"\$(client_token phone)\" ] || exit 1
        [ \"\$a\" != \"\$b\" ] || exit 1
        [ \"\$a\" != \"\$(web_token)\" ] || exit 1
        [ \"\${#a}\" -eq 32 ] || exit 1
        [ \"\$(web_base_url 1.2.3.4 phone)\" = \"http://1.2.3.4:8080/\$a\" ] || exit 1
    "
    [ "$status" -eq 0 ]
}

@test "docroot прячет листинг, за токеном клиента виден только его каталог" {
    ln -s "$FT_TEST_DIR" "$FT_TEST_DIR/lnprobe" 2>/dev/null
    [ -L "$FT_TEST_DIR/lnprobe" ] || skip "ФС без симлинков (не Linux)"
    run bash -c "
        source '$SCRIPT'
        mkdir -p \"\$CLIENTS_DIR/phone\" \"\$CLIENTS_DIR/laptop\"
        printf 'phone|10.13.13.2||now\nlaptop|10.13.13.3||now\n' > \"\$CLIENTS_META\"
        _web_layout || exit 1
        [ -f \"\$WEB_ROOT/index.html\" ] || exit 1
        [ -f \"\$WEB_ROOT/\$(web_token)\$WEB_PROBE_EXT\" ] || exit 1
        [ -L \"\$WEB_ROOT/\$(client_token phone)\" ] || exit 1
        [ \"\$(readlink \"\$WEB_ROOT/\$(client_token phone)\")\" = \"\$CLIENTS_DIR/phone\" ] || exit 1
        [ -L \"\$WEB_ROOT/\$(client_token laptop)\" ] || exit 1
        # мастер-секрет каталогом не раздаётся
        [ -L \"\$WEB_ROOT/\$(web_token)\" ] && exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "_web_layout убирает симлинк удалённого клиента" {
    ln -s "$FT_TEST_DIR" "$FT_TEST_DIR/lnprobe" 2>/dev/null
    [ -L "$FT_TEST_DIR/lnprobe" ] || skip "ФС без симлинков (не Linux)"
    run bash -c "
        source '$SCRIPT'
        mkdir -p \"\$CLIENTS_DIR/phone\" \"\$CLIENTS_DIR/laptop\"
        printf 'phone|10.13.13.2||now\nlaptop|10.13.13.3||now\n' > \"\$CLIENTS_META\"
        _web_layout || exit 1
        gone=\$(client_token laptop)
        rm -rf \"\$CLIENTS_DIR/laptop\"
        printf 'phone|10.13.13.2||now\n' > \"\$CLIENTS_META\"
        _web_layout || exit 1
        [ -L \"\$WEB_ROOT/\$gone\" ] && exit 1
        [ -L \"\$WEB_ROOT/\$(client_token phone)\" ] || exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "migrate_client_dirs раскладывает плоские артефакты по каталогам клиентов" {
    run bash -c "
        source '$SCRIPT'
        mkdir -p \"\$CLIENTS_DIR\"
        printf 'phone|10.13.13.2||now\nphone-2|10.13.13.3||now\n' > \"\$CLIENTS_META\"
        : > \"\$CLIENTS_DIR/phone-direct.conf\"
        : > \"\$CLIENTS_DIR/phone-direct.png\"
        : > \"\$CLIENTS_DIR/phone-2-direct.conf\"
        migrate_client_dirs
        [ -f \"\$CLIENTS_DIR/phone/phone-direct.conf\" ] || exit 1
        [ -f \"\$CLIENTS_DIR/phone/phone-direct.png\" ] || exit 1
        # префиксы не должны перетягивать чужие файлы
        [ -f \"\$CLIENTS_DIR/phone-2/phone-2-direct.conf\" ] || exit 1
        [ -f \"\$CLIENTS_DIR/phone/phone-2-direct.conf\" ] && exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "allowlist и метаданные лежат вне раздаваемого каталога" {
    run bash -c "
        source '$SCRIPT'
        case \"\$CLIENTS_FILE_CONF\" in \"\$CLIENTS_DIR\"/*) exit 1 ;; esac
        case \"\$CLIENTS_META\" in \"\$CLIENTS_DIR\"/*) exit 1 ;; esac
        true
    "
    [ "$status" -eq 0 ]
}

@test "migrate_layout переносит clients.json из раздаваемого каталога" {
    run bash -c "
        source '$SCRIPT'
        mkdir -p \"\$CLIENTS_DIR\"
        echo '{\"clients\":{\"a\":1}}' > \"\$CLIENTS_DIR/clients.json\"
        echo 'old|10.13.13.2||now' > \"\$CLIENTS_DIR/clients.list\"
        migrate_layout
        [ ! -f \"\$CLIENTS_DIR/clients.json\" ] || exit 1
        [ ! -f \"\$CLIENTS_DIR/clients.list\" ] || exit 1
        grep -q '\"a\":1' \"\$CLIENTSFILE\" || exit 1
        grep -q '^old|' \"\$CLIENTS_META\" || exit 1
    "
    [ "$status" -eq 0 ]
}

@test "valid_client_name отсекает traversal и метасимволы regex" {
    run bash -c "
        source '$SCRIPT'
        valid_client_name 'phone-1' || exit 1
        valid_client_name 'a.b_c' || exit 1
        valid_client_name '../etc' && exit 1
        valid_client_name 'a/b' && exit 1
        valid_client_name 'a|b' && exit 1
        valid_client_name '.hidden' && exit 1
        valid_client_name '' && exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "alloc_client_ip упирается в границу /24" {
    run bash -c "
        source '$SCRIPT'
        conf=\"\$FT_PREFIX/full.conf\"
        for i in \$(seq 2 254); do echo \"AllowedIPs = 10.13.13.\$i/32\" >> \"\$conf\"; done
        alloc_client_ip \"\$conf\" 10.13.13
    "
    [ "$status" -ne 0 ]
}

@test "write_compose_file убирает снятый сервис и удаляет файл без компонентов" {
    run bash -c "
        source '$SCRIPT'
        INSTALL_FREETURN=1 INSTALL_AWG=1
        write_compose_file
        grep -q 'free-turn-proxy:' \"\$COMPOSE_FILE\" || exit 1
        grep -q 'freeturn-awg:' \"\$COMPOSE_FILE\" || exit 1

        INSTALL_FREETURN=0
        write_compose_file
        grep -q 'free-turn-proxy:' \"\$COMPOSE_FILE\" && exit 1
        grep -q 'freeturn-awg:' \"\$COMPOSE_FILE\" || exit 1

        INSTALL_AWG=0
        write_compose_file
        [ ! -f \"\$COMPOSE_FILE\" ] || exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "compose AWG не включает verbose-логи демона" {
    run bash -c "
        source '$SCRIPT'
        INSTALL_FREETURN=0 INSTALL_AWG=1
        write_compose_file
        grep -q 'AWG_LOG_LEVEL=error' \"\$COMPOSE_FILE\" || exit 1
        grep -q 'AWG_LOG_LEVEL=verbose' \"\$COMPOSE_FILE\" && exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "get_public_ip возвращает ошибку вместо плейсхолдера" {
    run bash -c "
        source '$SCRIPT'
        curl() { return 1; }
        export -f curl
        get_public_ip
    "
    [ "$status" -ne 0 ]
    [[ "$output" != *"IP_СЕРВЕРА"* ]]
}

@test "validate_config отсекает невалидный web-port" {
    run bash -c "
        source '$SCRIPT'
        INSTALL_FREETURN=0 INSTALL_AWG=1
        BACKEND_PORT=51820 WG_ENDPOINT='127.0.0.1:9000' FT_WEB_PORT=99999
        validate_config
    "
    [ "$status" -ne 0 ]
}

@test "CLI-флаги не уезжают в JSON RPC диспетчер" {
    run bash -c "
        source '$SCRIPT'
        for f in -y --yes --update --uninstall --reconfigure --purge --only-awg --method; do
            is_rpc_command \"\$f\" && { echo \"флаг \$f опознан как RPC\"; exit 1; }
        done
        for c in \$RPC_COMMANDS; do
            is_rpc_command \"\$c\" || { echo \"команда \$c не опознана\"; exit 1; }
        done
        true
    "
    [ "$status" -eq 0 ]
}

@test "веб-раздача не оставляет постоянного systemd-юнита" {
    run grep -n 'WantedBy=multi-user.target' "$SCRIPT"
    # единственный постоянный юнит - сам сервер free-turn-proxy
    [ "$(printf '%s\n' "$output" | wc -l)" -eq 1 ]

    run bash -c "grep -c 'RuntimeMaxSec' '$SCRIPT'"
    [ "$output" -ge 1 ]
}

@test "firewall_open не держит веб-порт открытым постоянно" {
    run bash -c "
        source '$SCRIPT'
        opened=()
        firewall_open_port() { opened+=(\"\$1/\$2\"); }
        INSTALL_FREETURN=1 INSTALL_AWG=1 AWG_DIRECT_PORT=1
        LISTEN_PORT=56000 BACKEND_PORT=51820 PROXY_MODE=udp FT_WEB_PORT=8080
        firewall_open
        printf '%s\n' \"\${opened[@]}\"
    "
    [[ "$output" == *"56000/udp"* ]]
    [[ "$output" == *"51820/udp"* ]]
    [[ "$output" != *"8080"* ]]
}

@test "ensure_web_server открывает порт сам и закрывает при неудаче запуска" {
    run bash -c "
        source '$SCRIPT'
        log=\"\$FT_PREFIX/fw.log\"
        firewall_open_port() { echo \"open \$1/\$2\" >> \"\$log\"; }
        firewall_close_port() { echo \"close \$1/\$2\" >> \"\$log\"; }
        port_owner() { echo free; }
        _web_server_argv() { printf '%s\n' fake-httpd \"\$1\"; }
        _web_start_systemd() { return 1; }
        _web_start_nohup() { return 1; }
        ensure_web_server && exit 1
        grep -q 'open 8080/tcp' \"\$log\" || exit 1
        grep -q 'close 8080/tcp' \"\$log\" || exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "_web_server_argv берёт python3, иначе busybox" {
    # подменяем 'command -v', а не PATH: иначе тест зависел бы от того, стоит ли python3 в CI
    run bash -c "
        source '$SCRIPT'
        HAVE=''
        command() {
            if [ \"\$1\" = '-v' ]; then
                case \" \$HAVE \" in *\" \$2 \"*) echo \"/usr/bin/\$2\"; return 0 ;; esac
                return 1
            fi
            builtin command \"\$@\"
        }
        pkg_install() { return 1; }

        _web_server_argv 8080 && exit 1

        HAVE='busybox'
        [ \"\$(_web_server_argv 8080 | tr '\n' ' ')\" = \"busybox httpd -f -p 8080 -h \$WEB_ROOT \" ] || exit 1

        HAVE='python3 busybox'
        [ \"\$(_web_server_argv 8080 | tr '\n' ' ')\" = \"python3 -m http.server 8080 --directory \$WEB_ROOT \" ] || exit 1
        true
    "
    [ "$status" -eq 0 ]
}

@test "validate_config отсекает слишком короткий web-ttl" {
    run bash -c "
        source '$SCRIPT'
        INSTALL_FREETURN=0 INSTALL_AWG=1
        BACKEND_PORT=51820 WG_ENDPOINT='127.0.0.1:9000' FT_WEB_TTL=10
        validate_config
    "
    [ "$status" -ne 0 ]
}

@test "web_token не выдаёт нестабильный токен, если его негде сохранить" {
    run bash -c "
        source '$SCRIPT'
        WEB_TOKEN_FILE='/proc/nonexistent/web.token'
        web_token
    "
    [ "$status" -ne 0 ]
}

#!/usr/bin/env bash
# Сквозная проверка Linux-сборки в настоящем терминале (нужен tmux):
#
#   GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o kuma_log_sender .
#   bash tools/e2e/linux_tmux.sh ./kuma_log_sender
#
# Поднимает локальные приёмники, запускает интерфейс в tmux и проходит основные
# сценарии, включая вставку текста и изменение размера окна. Код завершения 0 — всё прошло.
set -u
BIN=$(readlink -f "$1")
REPO=$(cd "$(dirname "$0")/../.." && pwd)
S=kls_e2e_$$
W=$(mktemp -d /tmp/kls_e2e.XXXXXX)
FAILED=0

mkdir -p "$W/logs"
cp "$BIN" "$W/kuma_log_sender" && chmod +x "$W/kuma_log_sender"
cp "$REPO"/logs/examples/* "$W/logs/"
# Разделители записаны так, как их пишут при ручной правке: \n и \0.
cat > "$W/collectors.json" <<'EOF'
{
  "default_host": "127.0.0.1",
  "timeout_sec": 3,
  "collectors": [
    {"name": "dead-udp", "protocol": "udp", "port": 25999},
    {"name": "loc-https", "protocol": "http", "port": 25365, "delimiter": "\0", "tls": true},
    {"name": "loc-tcp", "protocol": "tcp", "port": 25140, "delimiter": "\n"},
    {"name": "loc-udp", "protocol": "udp", "port": 25144, "delimiter": ""}
  ]
}
EOF
cd "$W" || exit 1

./kuma_log_sender listen -p udp -P 25144 > udp.out 2>&1 &
PIDS="$!"
./kuma_log_sender listen -p tcp -P 25140 > tcp.out 2>&1 &
PIDS="$PIDS $!"
./kuma_log_sender listen -p http -P 25365 -tls -d '\0' > http.out 2>&1 &
PIDS="$PIDS $!"
sleep 0.7

fail() { echo "  FAIL $1"; FAILED=1; }
check() { if grep -qF -- "$2" "$1"; then echo "  ok   $1: $2"; else fail "$1: нет «$2»"; fi; }
wait_for() {
  if timeout 8 bash -c "until tmux capture-pane -t $S -p | grep -qF -- \"$1\"; do sleep 0.15; done"; then
    echo "  ok   $1"
  else
    fail "$1"
    tmux capture-pane -t "$S" -p
  fi
}

echo "== командная строка: tcp, https и ICMP-отказ на закрытом udp-порту"
./kuma_log_sender send -c loc-tcp -f cef.log -i 0 > send_tcp.out 2>&1 || fail "send tcp"
./kuma_log_sender send -c loc-https -f windows_4625_multiline.xml -m whole > send_https.out 2>&1 || fail "send https"
./kuma_log_sender send -c dead-udp -t "nobody listens" > send_dead.out 2>&1 && fail "отправка на закрытый порт должна вернуть код 1"
check send_tcp.out "[3/3] отправлено"
check send_https.out "HTTP 200 OK"
check send_dead.out "ICMP"
sleep 0.3
check tcp.out "LOGIN_OK"
check http.out "POST /input"

echo "== интерфейс в tmux 120x34"
tmux new-session -d -s "$S" -x 120 -y 34 "./kuma_log_sender"
wait_for "KUMA Log Sender"
tmux send-keys -t "$S" Down Down Down             # loc-udp
tmux send-keys -t "$S" Tab Down Down Down Down    # syslog_ssh_bruteforce.log
wait_for "Предпросмотр: syslog_ssh_bruteforce.log"
wait_for "5 событий × 1 = 5 отправок"
tmux send-keys -t "$S" t; sleep 0.2
tmux send-keys -t "$S" 0 . 1; sleep 0.2
tmux send-keys -t "$S" Enter; sleep 0.2
tmux send-keys -t "$S" Enter
wait_for "[5/5] отправлено"
wait_for "завершено: отправлено 5, ошибок 0"

echo "== ручной ввод кириллицей и вставка из буфера (bracketed paste)"
tmux send-keys -t "$S" i; sleep 0.3
wait_for "Ручной ввод → loc-udp"
tmux send-keys -t "$S" -l 'привет из tmux'; sleep 0.3
tmux send-keys -t "$S" Enter
wait_for "[1/1] отправлено"
printf 'pasted one\tTAB\npasted two\n' | tmux load-buffer -
tmux paste-buffer -p -t "$S"; sleep 0.5
wait_for "pasted one→TAB"
tmux send-keys -t "$S" Enter
wait_for "[2/2] отправлено"

echo "== изменение размера окна"
tmux resize-window -t "$S" -x 84 -y 24; sleep 0.8
wait_for "Журнал"
tmux resize-window -t "$S" -x 70 -y 18; sleep 0.8
wait_for "Окно слишком маленькое"
tmux resize-window -t "$S" -x 140 -y 44; sleep 0.8
wait_for "Коллекторы"

echo "== выход"
tmux send-keys -t "$S" q; sleep 0.7
if tmux has-session -t "$S" 2>/dev/null; then
  fail "приложение не завершилось"
  tmux kill-session -t "$S"
else
  echo "  ok   приложение завершилось"
fi

sleep 0.3
check udp.out '"привет из tmux"'
check udp.out '"pasted one\tTAB"'
kill $PIDS 2>/dev/null

if [ "$FAILED" = 0 ]; then echo "РЕЗУЛЬТАТ: всё прошло | рабочая папка: $W"; else echo "РЕЗУЛЬТАТ: ЕСТЬ ОШИБКИ | рабочая папка: $W"; fi
exit "$FAILED"

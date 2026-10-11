"""Сквозная проверка в настоящей консоли Windows.

    python tools/e2e/windows_console.py kuma_log_sender.exe

Поднимает локальные приёмники (подкоманда listen), запускает интерфейс в скрытой
консоли и проходит основные сценарии, сверяя экран и то, что реально получили
приёмники. Код завершения 0 — всё прошло.
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from condrv import Console

exe = os.path.abspath(sys.argv[1])
repo = os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
work = tempfile.mkdtemp(prefix="kls_e2e_")
shutil.copytree(os.path.join(repo, "logs", "examples"), os.path.join(work, "logs"))
with open(os.path.join(work, "collectors.json"), "w", encoding="utf-8") as f:
    json.dump({"default_host": "127.0.0.1", "timeout_sec": 3, "collectors": [
        {"name": "dead-tcp", "protocol": "tcp", "port": 15999, "delimiter": "\\n"},
        {"name": "loc-tcp", "protocol": "tcp", "port": 15140, "delimiter": "\\n"},
        {"name": "loc-udp", "protocol": "udp", "port": 15144, "delimiter": ""},
    ]}, f)

listeners, outputs = [], {}
for proto, port in (("udp", 15144), ("tcp", 15140)):
    outputs[proto] = os.path.join(work, proto + ".out")
    listeners.append(subprocess.Popen([exe, "listen", "-p", proto, "-P", str(port)],
                                      stdout=open(outputs[proto], "wb"), stderr=subprocess.STDOUT,
                                      creationflags=subprocess.CREATE_NO_WINDOW))
time.sleep(0.7)


def received(proto):
    with open(outputs[proto], encoding="utf-8") as f:
        return f.read()


ok = True
c = Console([exe, "--dir", work], cwd=work)
try:
    print("== запуск")
    ok &= c.expect("KUMA Log Sender", "Коллекторы", "loc-udp", "cef.log")

    print("== файл построчно и повтор одной клавишей")
    c.press("down", "down")             # loc-udp
    c.press("tab", "down", "down")      # cef.log
    ok &= c.expect("Предпросмотр: cef.log", "3 события × 1 = 3 отправки")
    c.press("t")
    c.type("0.1")
    c.press("enter")                    # интервал введён, фокус вернулся к списку файлов
    c.press("enter")                    # отправка
    ok &= c.expect("[3/3] отправлено", "завершено: отправлено 3, ошибок 0")
    c.press("enter")                    # повтор
    ok &= c.expect("#2")
    time.sleep(1.0)
    if received("udp").count("LOGIN_OK") != 2:
        print("  FAIL приёмник должен был получить файл дважды")
        ok = False

    print("== ручной ввод, горячая клавиша в русской раскладке (ш = i)")
    c.press("ш")
    ok &= c.expect("Ручной ввод → loc-udp")
    c.type("тест kuma: событие №1")
    c.press("enter")
    ok &= c.expect("[1/1] отправлено")

    print("== вставка потоком нажатий (консоль без bracketed paste)")
    c.paste("delete me\tquick\nsecond line: yes\n")
    ok &= c.expect("Ручной ввод → loc-udp", "delete me→quick", "second line: yes")
    c.press("enter")
    ok &= c.expect("[2/2] отправлено")
    if "dead-tcp" not in c.screen():
        print("  FAIL вставленный текст сработал как горячие клавиши")
        ok = False
    time.sleep(0.5)
    for want in ('"тест kuma: событие №1"', '"delete me\\tquick"', '"second line: yes"'):
        if want not in received("udp"):
            print("  FAIL приёмник не получил " + want)
            ok = False

    print("== цикл и остановка")
    c.press("l", "enter")
    ok &= c.expect("активных: 1", "идёт")
    time.sleep(0.6)
    c.press("s")
    ok &= c.expect("остановлена", "остановлено: отправлено")
    c.press("l")

    print("== ошибка с объяснением причины")
    c.press("left", "up", "up")         # dead-tcp
    c.press("enter")
    ok &= c.expect("НЕ отправлено", "порт закрыт")

    print("== новый коллектор через форму и отправка по TCP")
    c.press("a")
    ok &= c.expect("Новый коллектор")
    c.type("Новый TCP")
    c.press("down", "left", "down", "down")   # протокол tcp, затем поле «Порт»
    c.type("15140")
    c.press("enter")
    ok &= c.expect("Коллектор «Новый TCP» сохранён", "tcp://127.0.0.1:15140")
    c.press("enter")
    ok &= c.expect("отправлено → tcp://127.0.0.1:15140")
    time.sleep(0.5)
    if '"delete me\\tquick"' not in received("tcp"):
        print("  FAIL TCP-приёмник не получил событие")
        ok = False

    print("== выход")
    c.press("q")
    time.sleep(0.5)
    code = c.close()
    if code != 0:
        print(f"  FAIL код завершения {code}")
        ok = False
finally:
    for p in listeners:
        p.kill()

print("РЕЗУЛЬТАТ:", "всё прошло" if ok else "ЕСТЬ ОШИБКИ", "| рабочая папка:", work)
sys.exit(0 if ok else 1)

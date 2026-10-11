"""Драйвер для проверки интерфейса в настоящей консоли Windows.

Запускает программу в отдельной скрытой консоли, подключается к ней и умеет слать
нажатия клавиш через WriteConsoleInput и читать экран через
ReadConsoleOutputCharacter — программа работает так же, как при запуске
пользователем в обычной консоли Windows (conhost).

Размер скрытой консоли изменить нельзя, поэтому используется размер по умолчанию
(обычно 120×30).
"""
import ctypes
import subprocess
import sys
import time
from ctypes import wintypes

k32 = ctypes.WinDLL("kernel32", use_last_error=True)
u32 = ctypes.WinDLL("user32", use_last_error=True)

GENERIC_READ, GENERIC_WRITE = 0x80000000, 0x40000000
FILE_SHARE_READ, FILE_SHARE_WRITE = 1, 2
OPEN_EXISTING = 3
KEY_EVENT = 1
LEFT_CTRL, SHIFT = 0x0008, 0x0010

VK = {"enter": 0x0D, "tab": 0x09, "esc": 0x1B, "up": 0x26, "down": 0x28, "left": 0x25,
      "right": 0x27, "backspace": 0x08, "end": 0x23, "home": 0x24, "pgup": 0x21, "pgdn": 0x22,
      "f1": 0x70, "f5": 0x74, "space": 0x20}
CHARS = {"enter": "\r", "tab": "\t", "esc": "\x1b", "backspace": "\b", "space": " "}


class COORD(ctypes.Structure):
    _fields_ = [("X", wintypes.SHORT), ("Y", wintypes.SHORT)]


class SMALL_RECT(ctypes.Structure):
    _fields_ = [("Left", wintypes.SHORT), ("Top", wintypes.SHORT), ("Right", wintypes.SHORT), ("Bottom", wintypes.SHORT)]


class CSBI(ctypes.Structure):
    _fields_ = [("dwSize", COORD), ("dwCursorPosition", COORD), ("wAttributes", wintypes.WORD),
                ("srWindow", SMALL_RECT), ("dwMaximumWindowSize", COORD)]


class KEY_EVENT_RECORD(ctypes.Structure):
    _fields_ = [("bKeyDown", wintypes.BOOL), ("wRepeatCount", wintypes.WORD), ("wVirtualKeyCode", wintypes.WORD),
                ("wVirtualScanCode", wintypes.WORD), ("uChar", wintypes.WCHAR), ("dwControlKeyState", wintypes.DWORD)]


class _U(ctypes.Union):
    _fields_ = [("KeyEvent", KEY_EVENT_RECORD), ("pad", ctypes.c_byte * 16)]


class INPUT_RECORD(ctypes.Structure):
    _fields_ = [("EventType", wintypes.WORD), ("u", _U)]


k32.CreateFileW.restype = wintypes.HANDLE
k32.CreateFileW.argtypes = [wintypes.LPCWSTR, wintypes.DWORD, wintypes.DWORD, ctypes.c_void_p, wintypes.DWORD, wintypes.DWORD, wintypes.HANDLE]
k32.ReadConsoleOutputCharacterW.argtypes = [wintypes.HANDLE, wintypes.LPWSTR, wintypes.DWORD, COORD, ctypes.POINTER(wintypes.DWORD)]
k32.WriteConsoleInputW.argtypes = [wintypes.HANDLE, ctypes.POINTER(INPUT_RECORD), wintypes.DWORD, ctypes.POINTER(wintypes.DWORD)]
k32.GetConsoleScreenBufferInfo.argtypes = [wintypes.HANDLE, ctypes.POINTER(CSBI)]


class Console:
    def __init__(self, argv, cwd=None):
        si = subprocess.STARTUPINFO()
        si.dwFlags |= subprocess.STARTF_USESHOWWINDOW
        si.wShowWindow = 0  # SW_HIDE
        self.proc = subprocess.Popen(argv, creationflags=subprocess.CREATE_NEW_CONSOLE, startupinfo=si, cwd=cwd)
        time.sleep(0.8)
        k32.FreeConsole()
        if not k32.AttachConsole(self.proc.pid):
            raise OSError(ctypes.get_last_error(), "AttachConsole")
        self.hin = k32.CreateFileW("CONIN$", GENERIC_READ | GENERIC_WRITE, FILE_SHARE_READ | FILE_SHARE_WRITE, None, OPEN_EXISTING, 0, None)

    def _out(self):
        # Программа переключается на альтернативный буфер экрана, поэтому
        # CONOUT$ открываем каждый раз заново — он указывает на активный буфер.
        return k32.CreateFileW("CONOUT$", GENERIC_READ | GENERIC_WRITE, FILE_SHARE_READ | FILE_SHARE_WRITE, None, OPEN_EXISTING, 0, None)

    def size(self):
        h = self._out()
        info = CSBI()
        k32.GetConsoleScreenBufferInfo(h, ctypes.byref(info))
        k32.CloseHandle(h)
        w = info.srWindow
        return w.Right - w.Left + 1, w.Bottom - w.Top + 1, w.Top

    def screen(self):
        cols, rows, top = self.size()
        h = self._out()
        lines = []
        buf = ctypes.create_unicode_buffer(cols + 1)
        n = wintypes.DWORD()
        for y in range(rows):
            k32.ReadConsoleOutputCharacterW(h, buf, cols, COORD(0, top + y), ctypes.byref(n))
            lines.append(buf.value[: n.value].rstrip())
        k32.CloseHandle(h)
        return "\n".join(lines)

    def _send(self, records):
        arr = (INPUT_RECORD * len(records))(*records)
        n = wintypes.DWORD()
        if not k32.WriteConsoleInputW(self.hin, arr, len(records), ctypes.byref(n)):
            raise OSError(ctypes.get_last_error(), "WriteConsoleInput")

    @staticmethod
    def _key(vk, ch, mods=0):
        recs = []
        for down in (True, False):
            r = INPUT_RECORD()
            r.EventType = KEY_EVENT
            r.u.KeyEvent = KEY_EVENT_RECORD(down, 1, vk, u32.MapVirtualKeyW(vk, 0) if vk else 0, ch, mods)
            recs.append(r)
        return recs

    def _char(self, ch):
        if ch == "\n":
            return self._key(VK["enter"], "\r")
        if ch == "\t":
            return self._key(VK["tab"], "\t")
        scan = u32.VkKeyScanW(ord(ch))
        if scan in (-1, 0xFFFF) or scan & 0xFF == 0xFF:
            return self._key(0, ch)  # символа нет в текущей раскладке — как при вставке
        mods = SHIFT if (scan >> 8) & 1 else 0
        return self._key(scan & 0xFF, ch, mods)

    def press(self, *names, delay=0.25):
        """Нажатия с человеческой скоростью: имена клавиш или одиночные символы."""
        for name in names:
            if name.startswith("ctrl+"):
                letter = name[5:]
                self._send(self._key(ord(letter.upper()), chr(ord(letter.lower()) - 96), LEFT_CTRL))
            elif name in VK:
                self._send(self._key(VK[name], CHARS.get(name, "\0")))
            else:
                self._send(self._char(name))
            time.sleep(delay)

    def type(self, text, delay=0.06):
        for ch in text:
            self._send(self._char(ch))
            time.sleep(delay)
        time.sleep(0.2)

    def paste(self, text):
        """Поток нажатий одним пакетом — так старая консоль вставляет текст из буфера обмена."""
        recs = []
        for ch in text:
            recs += self._char(ch)
        self._send(recs)
        time.sleep(0.5)

    def wait(self, needle, timeout=6.0):
        deadline = time.time() + timeout
        while time.time() < deadline:
            if needle in self.screen():
                return True
            time.sleep(0.1)
        return False

    def expect(self, *needles, timeout=6.0):
        """Ждёт появления каждой строки на экране; при неудаче печатает экран."""
        ok = True
        for needle in needles:
            found = self.wait(needle, timeout)
            print(("  ok   " if found else "  FAIL ") + needle)
            ok = ok and found
        if not ok:
            print(self.screen())
        sys.stdout.flush()
        return ok

    def close(self):
        try:
            self.proc.wait(timeout=3)
        except subprocess.TimeoutExpired:
            self.proc.kill()
            return None
        return self.proc.returncode


sys.stdout.reconfigure(encoding="utf-8")

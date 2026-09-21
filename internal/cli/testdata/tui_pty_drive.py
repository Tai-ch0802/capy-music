#!/usr/bin/env python3
"""在 pty 裡跑真的 capy、用終端機模擬器(pyte)把輸出畫出來,看 inline renderer 實際留下什麼。

單元測試量得到 View 的內容,量不到 renderer 怎麼把它畫上終端機——「水豚頭重複」「畫面變矮留殘留」「收回終端機時
蓋掉子命令的輸出」都只在真的終端機裡發生(docs/superpowers/plans/2026-09-21-capybara-alive.md §3)。
手動工具,不進 CI:會用到本機真的設定與帳號,所以只送唯讀的命令。

    python3 -m venv /tmp/v && /tmp/v/bin/pip install pyte
    /tmp/v/bin/python tui_pty_drive.py ./capy 100 40 wait:4 'keys:/config list\\r' wait:2 dump:after

步驟:wait:<秒>  keys:<字串,\\r \\x1b 這類跳脫會解開>  resize:<欄>x<列>  dump:<標題>
dump 會印捲動區 + 畫面,並數「水豚的腳」那一行出現幾次——應該恰好一次(執行子命令的當下是零次)。
結束時送 SIGTERM;三秒內沒結束會講出來,設 STACKS=1 的話再送 SIGQUIT 把 goroutine 堆疊印出來。
"""
import fcntl, os, pty, re, select, signal, struct, sys, termios, time

import pyte

exe, cols, rows, steps = sys.argv[1], int(sys.argv[2]), int(sys.argv[3]), sys.argv[4:]
screen = pyte.HistoryScreen(cols, rows, history=2000, ratio=1.0)
stream = pyte.ByteStream(screen)

pid, fd = pty.fork()
if pid == 0:
    os.environ["TERM"] = "xterm-256color"
    try:
        os.execv(exe, [exe])
    finally:  # execv 失敗(路徑打錯、還沒 build)的話 child 不可以掉下去跑下面的迴圈:兩個行程搶同一個 fd,看起來像 capy 壞了
        os._exit(127)
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))


def pump(sec):
    end = time.time() + sec
    while time.time() < end:
        if not select.select([fd], [], [], 0.05)[0]:
            continue
        try:
            data = os.read(fd, 65536)
        except OSError:
            return
        if not data:
            return
        if b"\x1b[6n" in data:  # bubbletea 開場會問游標位置;不回答它會等到逾時
            os.write(fd, b"\x1b[%d;1R" % (screen.cursor.y + 1))
        # pyte 不認得 kitty 鍵盤協定的 CSI ... u,會把尾巴當文字印出來、游標跟著歪
        data = re.sub(rb"\x1b\[[>=<?][0-9;]*u", b"", data)
        # 也不認得 CBT(CSI n Z,往回跳 n 個 tab 停駐點),而 uv 的 renderer 會用它省位元組:自己補上
        pos = 0
        for m in re.finditer(rb"\x1b\[(\d*)Z", data):
            stream.feed(data[pos:m.start()])
            pos = m.end()
            for _ in range(int(m.group(1) or 1)):
                screen.cursor.x = max(0, ((screen.cursor.x - 1) // 8) * 8)
        stream.feed(data[pos:])


def dump(label):
    hist = ["".join(line[x].data for x in range(cols)).rstrip() for line in screen.history.top]
    lines = hist + [l.rstrip() for l in screen.display]
    feet = sum("|__|            |__|" in l for l in lines)
    print(f"===== {label}:捲動區 {len(hist)} 行 + 畫面 {rows} 行;水豚的腳出現 {feet} 次 =====")
    while lines and not lines[-1]:
        lines.pop()
    for l in lines[-int(os.environ.get("TAIL", "40")):]:
        print("  |" + l)


for st in steps:
    kind, _, arg = st.partition(":")
    if kind == "wait":
        pump(float(arg))
    elif kind == "keys":
        for ch in arg.encode().decode("unicode_escape"):
            os.write(fd, ch.encode())
            pump(0.08)
    elif kind == "resize":
        cols, rows = map(int, arg.split("x"))
        screen.resize(rows, cols)
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        os.kill(pid, signal.SIGWINCH)
        pump(0.5)
    elif kind == "dump":
        dump(arg)
# 收屍(不收的話連跑幾個情境會留一串 zombie)。等不到就是 capy 卡住了——這本身就是要抓的症狀,所以講出來、不要陪它卡。
try:
    os.kill(pid, signal.SIGTERM)
    for _ in range(60):
        if os.waitpid(pid, os.WNOHANG)[0]:
            break
        pump(0.05)
    else:
        print("!!!!! capy 收到 SIGTERM 三秒還沒結束(卡住了),改用 SIGKILL")
        if os.environ.get("STACKS"):  # SIGQUIT:Go runtime 把每個 goroutine 的堆疊印到 stderr(就是這個 pty)再結束
            os.kill(pid, signal.SIGQUIT)
            end, raw = time.time() + 2, b""
            while time.time() < end:
                if select.select([fd], [], [], 0.1)[0]:
                    try:
                        raw += os.read(fd, 65536)
                    except OSError:
                        break
            print(raw.decode("utf-8", "replace").replace("\r", ""))
        os.kill(pid, signal.SIGKILL)
        os.waitpid(pid, 0)
except (ProcessLookupError, ChildProcessError):
    pass

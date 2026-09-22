#!/usr/bin/env python3
"""在 pty 裡跑真的 capy、用終端機模擬器(pyte)把輸出畫出來,看 inline renderer 實際留下什麼。

單元測試量得到 View 的內容,量不到 renderer 怎麼把它畫上終端機——「水豚頭重複」「畫面變矮留殘留」「收回終端機時
蓋掉子命令的輸出」都只在真的終端機裡發生(docs/superpowers/plans/2026-09-21-capybara-alive.md §3)。
手動工具,不進 CI:會用到本機真的設定與帳號,所以只送唯讀的命令。

    python3 -m venv /tmp/v && /tmp/v/bin/pip install pyte
    /tmp/v/bin/python tui_pty_drive.py ./capy 100 40 wait:4 'keys:/config list\\r' wait:2 dump:after
    /tmp/v/bin/python tui_pty_drive.py './capy now --watch' 100 40 wait:3 sigint:5    # 第一個參數可以帶子命令

步驟:wait:<秒>  keys:<字串,\\r \\x1b 這類跳脫會解開>  resize:<欄>x<列>  dump:<標題>
      sigint:<秒>(對前景行程群組送 SIGINT,量多久結束,最多等這麼久)
dump 會印捲動區 + 畫面,並數「水豚的腳」那一行出現幾次——應該恰好一次(執行子命令的當下是零次)。
結束時送 SIGTERM;三秒內沒結束會講出來,設 STACKS=1 的話再送 SIGQUIT 把 goroutine 堆疊印出來。
行程結束時印它的結束碼:q / 做完 = 0、SIGINT = 130、SIGTERM = 143(負數 = 被沒接住的訊號殺掉,例如 -9)。
只量結束碼 / 訊號的話不用裝 pyte(系統的 python3 就能跑);dump 才需要它。
"""
import fcntl, os, pty, re, select, shlex, signal, struct, sys, termios, time

try:
    import pyte
except ImportError:
    pyte = None

argv, cols, rows, steps = shlex.split(sys.argv[1]), int(sys.argv[2]), int(sys.argv[3]), sys.argv[4:]
screen = pyte.HistoryScreen(cols, rows, history=2000, ratio=1.0) if pyte else None
stream = pyte.ByteStream(screen) if pyte else None

pid, fd = pty.fork()
if pid == 0:
    os.environ["TERM"] = "xterm-256color"
    try:
        os.execv(argv[0], argv)
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
            os.write(fd, b"\x1b[%d;1R" % (screen.cursor.y + 1 if screen else 1))
        if not screen:
            continue
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
    if not screen:
        print(f"===== {label}:沒有 pyte,畫不出畫面(pip install pyte)=====")
        return
    hist = ["".join(line[x].data for x in range(cols)).rstrip() for line in screen.history.top]
    lines = hist + [l.rstrip() for l in screen.display]
    feet = sum("|__|            |__|" in l for l in lines)
    print(f"===== {label}:捲動區 {len(hist)} 行 + 畫面 {rows} 行;水豚的腳出現 {feet} 次 =====")
    while lines and not lines[-1]:
        lines.pop()
    for l in lines[-int(os.environ.get("TAIL", "40")):]:
        print("  |" + l)


exited = False


def reap(flags=os.WNOHANG):  # 回「行程結束了沒」;結束後再叫不會再 waitpid(兩個 sigint: 步驟會 ChildProcessError)
    global exited
    if exited:
        return True
    got, status = os.waitpid(pid, flags)
    if got:
        exited = True
        print(f"===== 結束碼 {os.waitstatus_to_exitcode(status)} =====")
    return exited


for st in steps:
    kind, _, arg = st.partition(":")
    if kind == "wait":
        pump(float(arg))
    elif exited and kind in ("keys", "resize", "sigint"):  # 行程已經收掉了:再送東西只會丟 ProcessLookupError / OSError
        print(f"===== capy 已經結束,跳過 {st} =====")
    elif kind == "keys":
        for ch in arg.encode().decode("unicode_escape"):
            os.write(fd, ch.encode())
            pump(0.08)
    elif kind == "resize":
        cols, rows = map(int, arg.split("x"))
        if screen:
            screen.resize(rows, cols)
        fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", rows, cols, 0, 0))
        os.kill(pid, signal.SIGWINCH)
        pump(0.5)
    elif kind == "sigint":  # 對整個前景行程群組送 SIGINT(子命令跑著的時候終端機在 cooked mode,Ctrl-C 就是這樣到的),再量多久結束
        os.killpg(os.getpgid(pid), signal.SIGINT)
        t0, limit = time.time(), float(arg or 5)
        while not reap() and time.time() - t0 < limit:
            pump(0.05)
        print("===== SIGINT 之後 " + (f"{time.time() - t0:.1f} 秒結束" if exited else f"{limit:g} 秒還沒結束") + " =====")
    elif kind == "dump":
        dump(arg)
# 收屍(不收的話連跑幾個情境會留一串 zombie)。等不到就是 capy 卡住了——這本身就是要抓的症狀,所以講出來、不要陪它卡。
if not exited:
    try:
        os.kill(pid, signal.SIGTERM)
        for _ in range(60):
            if reap():
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
            reap(0)
    except (ProcessLookupError, ChildProcessError):
        pass

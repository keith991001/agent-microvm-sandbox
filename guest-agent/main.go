// guest-agent：microVM 内部で動く（Linux/arm64）。
// vsock で JSON リクエスト {cmd, stdin, timeout_ms} を受け取り、tmpfs 上で bash 実行、
// {stdout, stderr, exit, timed_out, duration_ms} を JSON で返し、電源を切る。
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	vsockPort        = 1024
	defaultTimeoutMs = 30000
)

type request struct {
	Cmd       string `json:"cmd"`
	Stdin     string `json:"stdin"`
	TimeoutMs int    `json:"timeout_ms"`
}

type result struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	Exit       int    `json:"exit"`
	TimedOut   bool   `json:"timed_out"`
	DurationMs int64  `json:"duration_ms"`
}

func main() {
	_ = unix.Mount("proc", "/proc", "proc", 0, "")
	_ = unix.Mount("sysfs", "/sys", "sysfs", 0, "")
	_ = unix.Mount("tmpfs", "/tmp", "tmpfs", 0, "mode=1777") // 使い捨ての書き込み領域

	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		bail("socket: " + err.Error())
	}
	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: vsockPort}); err != nil {
		bail("bind: " + err.Error())
	}
	if err := unix.Listen(fd, 1); err != nil {
		bail("listen: " + err.Error())
	}
	fmt.Println("[agent] listening on vsock port", vsockPort)

	nfd, _, err := unix.Accept(fd)
	if err != nil {
		bail("accept: " + err.Error())
	}
	conn := os.NewFile(uintptr(nfd), "vsock-conn")

	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(result{Stderr: "bad request: " + err.Error(), Exit: 127})
		shutdown(conn)
	}

	_ = json.NewEncoder(conn).Encode(run(req))
	shutdown(conn)
}

// run はジャッジ向け：stdin を渡し、時間を計測し、タイムアウトはプロセスグループごと kill
func run(req request) result {
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if req.TimeoutMs <= 0 {
		timeout = defaultTimeoutMs * time.Millisecond
	}

	var so, se bytes.Buffer
	c := exec.Command("/bin/bash", "-c", req.Cmd)
	c.Dir = "/tmp"
	c.Env = []string{
		"HOME=/tmp", "TMPDIR=/tmp", "TERM=dumb",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	c.Stdin = strings.NewReader(req.Stdin) // テスト入力を標準入力へ
	c.Stdout = &so
	c.Stderr = &se
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	start := time.Now()
	res := result{}
	if err := c.Start(); err != nil {
		return result{Stderr: err.Error(), Exit: 127}
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case werr := <-done:
		if ee, ok := werr.(*exec.ExitError); ok {
			res.Exit = ee.ExitCode()
		} else if werr != nil {
			res.Exit, se = 127, *bytes.NewBufferString(se.String()+werr.Error())
		}
	case <-time.After(timeout):
		syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
		<-done
		res.Exit = 124
		res.TimedOut = true
	}
	res.DurationMs = time.Since(start).Milliseconds()
	res.Stdout, res.Stderr = so.String(), se.String()
	return res
}

func shutdown(conn *os.File) {
	conn.Close()
	unix.Sync()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	os.Exit(0)
}

func bail(msg string) {
	fmt.Fprintln(os.Stderr, "[agent] FATAL:", msg)
	unix.Sync()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	os.Exit(1)
}

// guest-agent：在 microVM 内部运行（Linux/arm64）。
// 监听 vsock，收一条命令，在 tmpfs 临时区里用 bash 执行（带超时、整组终止），
// 把 stdout/stderr/退出码 以 JSON 回传，然后关机。
package main

import (
	"bufio"
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
	vsockPort  = 1024
	cmdTimeout = 30 * time.Second
)

type result struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Exit   int    `json:"exit"`
}

func main() {
	// 命令运行所需的挂载
	_ = unix.Mount("proc", "/proc", "proc", 0, "")
	_ = unix.Mount("sysfs", "/sys", "sysfs", 0, "")
	// 可写临时区：tmpfs 挂在已存在的 /tmp 上（根只读，但其上的 tmpfs 可写、关机即清空）
	_ = unix.Mount("tmpfs", "/tmp", "tmpfs", 0, "mode=1777")

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

	cmd, _ := bufio.NewReader(conn).ReadString('\n')
	cmd = strings.TrimRight(cmd, "\r\n")

	res := run(cmd)

	_ = json.NewEncoder(conn).Encode(res)
	conn.Close()

	unix.Sync()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
}

// run 在 tmpfs 里用 bash 执行命令，带超时与整组终止
func run(cmd string) result {
	var so, se bytes.Buffer
	c := exec.Command("/bin/bash", "-c", cmd)
	c.Dir = "/tmp"
	c.Env = []string{
		"HOME=/tmp", "TMPDIR=/tmp", "TERM=dumb",
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	c.Stdout = &so
	c.Stderr = &se
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // 独立进程组，便于整组杀

	exit := 0
	if err := c.Start(); err != nil {
		se.WriteString(err.Error())
		return result{so.String(), se.String(), 127}
	}

	done := make(chan error, 1)
	go func() { done <- c.Wait() }()

	select {
	case werr := <-done:
		if ee, ok := werr.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else if werr != nil {
			exit = 127
			se.WriteString(werr.Error())
		}
	case <-time.After(cmdTimeout):
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL) // 杀整个进程组（含子孙）
		<-done
		exit = 124
		se.WriteString(fmt.Sprintf("\n[agent] 命令超时（>%s）已被终止\n", cmdTimeout))
	}
	return result{so.String(), se.String(), exit}
}

func bail(msg string) {
	fmt.Fprintln(os.Stderr, "[agent] FATAL:", msg)
	unix.Sync()
	_ = unix.Reboot(unix.LINUX_REBOOT_CMD_POWER_OFF)
	os.Exit(1)
}

package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Code-Hex/vz/v3"
)

const (
	kernelPath = "vm/vmlinux"
	initrdPath = "vm/initrd"
	rootfsPath = "vm/rootfs.raw"
	sharePath  = "share"
	shareTag   = "agentshare"
	vsockPort  = 1024

	kernelCmdline = "console=hvc0 root=/dev/vda1 ro rootflags=noload init=/bin/sh"
	readyMark     = "__VM_READY__"
)

// request：判題/コード実行のリクエスト
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

// vmInstance 是一台已经启动、agent 正在 vsock 上监听的（热）虚拟机
type vmInstance struct {
	vm  *vz.VirtualMachine
	inW *os.File
}

func main() {
	serve := flag.Bool("serve", false, "作为 HTTP 服务运行（带预热池）")
	addr := flag.String("addr", ":8080", "HTTP 监听地址")
	poolN := flag.Int("pool", 2, "预热池大小")
	timeoutMs := flag.Int("timeout", 30000, "命令超时（毫秒）")
	flag.Parse()
	debug := os.Getenv("VM_DEBUG") != ""

	if *serve {
		runServer(*addr, *poolN, debug)
		return
	}

	// 一次性模式：./microvm [-timeout ms] "命令"
	// 测试输入可通过管道喂给程序的 stdin： echo "5 3" | ./microvm "python3 solve.py"
	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, `用法: microvm [-timeout ms] "<命令>"   或   microvm -serve [-pool N] [-addr :8080]`)
		os.Exit(2)
	}
	var stdin string
	if fi, _ := os.Stdin.Stat(); fi != nil && fi.Mode()&os.ModeCharDevice == 0 {
		b, _ := io.ReadAll(os.Stdin) // 管道/重定向输入 → 作为程序 stdin
		stdin = string(b)
	}
	inst, err := bootVM(debug)
	if err != nil {
		fatal("boot: " + err.Error())
	}
	res, err := inst.exec(request{Cmd: args[0], Stdin: stdin, TimeoutMs: *timeoutMs})
	if err != nil {
		fatal("exec: " + err.Error())
	}
	fmt.Print(res.Stdout)
	if res.Stderr != "" {
		fmt.Fprint(os.Stderr, res.Stderr)
	}
	verdict := ""
	if res.TimedOut {
		verdict = " TIMEOUT"
	}
	fmt.Fprintf(os.Stderr, "[exit=%d  time=%dms%s]\n", res.Exit, res.DurationMs, verdict)
}

// ---------------- HTTP 服务 (S7) ----------------

func runServer(addr string, n int, debug bool) {
	p := newPool(n, debug)
	http.HandleFunc("/run", func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Cmd == "" {
			http.Error(w, `需要 JSON: {"cmd":"...","stdin":"...","timeout_ms":N}`, http.StatusBadRequest)
			return
		}
		inst := p.get() // 从热池取一台（已开好机）
		res, err := inst.exec(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})
	log.Printf("microVM sandbox 服务已启动: %s （预热池=%d）", addr, n)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// ---------------- 预热池 (S6) ----------------

type pool struct {
	ch    chan *vmInstance
	debug bool
}

func newPool(n int, debug bool) *pool {
	p := &pool{ch: make(chan *vmInstance, n), debug: debug}
	for i := 0; i < n; i++ {
		go p.spawn()
	}
	return p
}

// spawn 后台开一台热 VM 放进池子（失败则重试）
func (p *pool) spawn() {
	for {
		inst, err := bootVM(p.debug)
		if err != nil {
			log.Println("预热 VM 启动失败，重试:", err)
			time.Sleep(time.Second)
			continue
		}
		p.ch <- inst
		return
	}
}

// get 取一台热 VM，并立刻在后台补充一台
func (p *pool) get() *vmInstance {
	inst := <-p.ch
	go p.spawn()
	return inst
}

// ---------------- VM 生命周期 ----------------

// bootVM 启动一台 VM 并完成引导，直到 agent 在 vsock 上 listen（即“热”）
func bootVM(debug bool) (*vmInstance, error) {
	inR, inW, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, err
	}

	config, err := buildConfig(inR, outW)
	if err != nil {
		return nil, err
	}
	vm, err := vz.NewVirtualMachine(config)
	if err != nil {
		return nil, err
	}

	ready := make(chan struct{}, 1)     // PID1 的 sh 开始读串口
	listening := make(chan struct{}, 1) // agent 已 listen
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			line := strings.TrimRight(sc.Text(), "\r")
			if debug {
				fmt.Fprintln(os.Stderr, "GUEST|", line)
			}
			if strings.Contains(line, readyMark) && !strings.Contains(line, "echo") {
				trySig(ready)
			}
			if strings.Contains(line, "listening on vsock") {
				trySig(listening)
			}
		}
	}()

	if err := vm.Start(); err != nil {
		return nil, err
	}

	// 等 sh 就绪（早发会被丢，重试）
	for waiting := true; waiting; {
		fmt.Fprintf(inW, "\necho %s\n", readyMark)
		select {
		case <-ready:
			waiting = false
		case <-time.After(200 * time.Millisecond):
		}
	}

	// 串口引导：加载驱动 -> 挂 virtiofs -> 启动 agent
	fmt.Fprint(inW, strings.Join([]string{
		"modprobe virtiofs 2>/dev/null",
		"modprobe vmw_vsock_virtio_transport 2>/dev/null",
		"mount -t virtiofs " + shareTag + " /mnt",
		"/mnt/agent",
		"",
	}, "\n"))

	// 等 agent listen 起来（这时 VM 就“热”了）
	select {
	case <-listening:
		return &vmInstance{vm: vm, inW: inW}, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("agent 未在限定时间内启动")
	}
}

// exec 向热 VM 发 JSON 请求，收 JSON 结果（VM 执行完会自己 poweroff）
func (inst *vmInstance) exec(req request) (result, error) {
	devs := inst.vm.SocketDevices()
	if len(devs) == 0 {
		return result{}, fmt.Errorf("无 socket 设备")
	}
	var conn *vz.VirtioSocketConnection
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := devs[0].Connect(vsockPort)
		if err == nil {
			conn = c
			break
		}
		if time.Now().After(deadline) {
			return result{}, fmt.Errorf("vsock 连接超时: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	if err := json.NewEncoder(conn).Encode(req); err != nil {
		conn.Close()
		return result{}, err
	}
	var res result
	derr := json.NewDecoder(conn).Decode(&res)
	conn.Close()
	go waitStopped(inst.vm) // 后台等它关机，不阻塞返回
	return res, derr
}

func waitStopped(vm *vz.VirtualMachine) {
	timeout := time.After(40 * time.Second)
	for {
		select {
		case st := <-vm.StateChangedNotify():
			if st == vz.VirtualMachineStateStopped || st == vz.VirtualMachineStateError {
				return
			}
		case <-timeout:
			if vm.CanStop() {
				_ = vm.Stop()
			}
			return
		}
	}
}

// buildConfig 组装一台 VM 的配置（串口接到给定的管道）
func buildConfig(inR, outW *os.File) (*vz.VirtualMachineConfiguration, error) {
	bootLoader, err := vz.NewLinuxBootLoader(kernelPath, vz.WithCommandLine(kernelCmdline), vz.WithInitrd(initrdPath))
	if err != nil {
		return nil, err
	}
	config, err := vz.NewVirtualMachineConfiguration(bootLoader, 2, 1*1024*1024*1024)
	if err != nil {
		return nil, err
	}

	serialAtt, err := vz.NewFileHandleSerialPortAttachment(inR, outW)
	if err != nil {
		return nil, err
	}
	consoleCfg, err := vz.NewVirtioConsoleDeviceSerialPortConfiguration(serialAtt)
	if err != nil {
		return nil, err
	}
	config.SetSerialPortsVirtualMachineConfiguration([]*vz.VirtioConsoleDeviceSerialPortConfiguration{consoleCfg})

	rootDev, err := blockDevice(rootfsPath, true)
	if err != nil {
		return nil, err
	}
	config.SetStorageDevicesVirtualMachineConfiguration([]vz.StorageDeviceConfiguration{rootDev})

	entropy, err := vz.NewVirtioEntropyDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	config.SetEntropyDevicesVirtualMachineConfiguration([]*vz.VirtioEntropyDeviceConfiguration{entropy})

	sockDev, err := vz.NewVirtioSocketDeviceConfiguration()
	if err != nil {
		return nil, err
	}
	config.SetSocketDevicesVirtualMachineConfiguration([]vz.SocketDeviceConfiguration{sockDev})

	fsCfg, err := vz.NewVirtioFileSystemDeviceConfiguration(shareTag)
	if err != nil {
		return nil, err
	}
	shared, err := vz.NewSharedDirectory(sharePath, true)
	if err != nil {
		return nil, err
	}
	single, err := vz.NewSingleDirectoryShare(shared)
	if err != nil {
		return nil, err
	}
	fsCfg.SetDirectoryShare(single)
	config.SetDirectorySharingDevicesVirtualMachineConfiguration([]vz.DirectorySharingDeviceConfiguration{fsCfg})

	if ok, err := config.Validate(); err != nil || !ok {
		return nil, fmt.Errorf("配置无效: ok=%v err=%v", ok, err)
	}
	return config, nil
}

func blockDevice(path string, readOnly bool) (*vz.VirtioBlockDeviceConfiguration, error) {
	att, err := vz.NewDiskImageStorageDeviceAttachment(path, readOnly)
	if err != nil {
		return nil, err
	}
	return vz.NewVirtioBlockDeviceConfiguration(att)
}

func trySig(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func fatal(msg string) {
	fmt.Fprintln(os.Stderr, "FATAL:", msg)
	os.Exit(1)
}

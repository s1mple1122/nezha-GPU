package monitor

import (
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dean2021/goss"
	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/host"
	"github.com/shirou/gopsutil/v3/load"
	"github.com/shirou/gopsutil/v3/mem"
	"github.com/shirou/gopsutil/v3/net"
	"github.com/shirou/gopsutil/v3/process"

	"github.com/naiba/nezha/model"
)

var (
	Version           string
	expectDiskFsTypes = []string{
		"apfs", "ext4", "ext3", "ext2", "f2fs", "reiserfs", "jfs", "btrfs",
		"fuseblk", "zfs", "simfs", "ntfs", "fat32", "exfat", "xfs", "fuse.rclone",
	}
	excludeNetInterfaces = []string{
		"lo", "tun", "docker", "veth", "br-", "vmbr", "vnet", "kube",
	}
)

var (
	netInSpeed, netOutSpeed, netInTransfer, netOutTransfer, lastUpdateNetStats uint64
	cachedBootTime                                                             time.Time
)

func gpuHave() int {
	cmd := exec.Command(`/bin/bash`, `-c`, `lspci -vnn | grep VGA |grep -i nvi | wc -l`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0
	}
	if err := cmd.Start(); err != nil {
		return 0
	}
	bytes, err := io.ReadAll(stdout)
	if err != nil {
		return 0
	}
	if err := cmd.Wait(); err != nil {
		return 0
	}
	s := strings.TrimSpace(string(bytes))
	num, _ := strconv.Atoi(s)
	return num
}

func gpuUsed() []uint64 {
	news := make([]uint64, 0)
	cmd := exec.Command(`/bin/bash`, `-c`, `nvidia-smi -a |grep Gpu |awk -F : '{print $2}'`)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return news
	}
	if err := cmd.Start(); err != nil {
		return news
	}
	bytes, err := io.ReadAll(stdout)
	if err != nil {
		return news
	}
	if err := cmd.Wait(); err != nil {
		return news
	}

	s := strings.Split(string(bytes), `%`)
	digitRegex := regexp.MustCompile(`^\d+$`)
	for _, v := range s {
		if digitRegex.MatchString(strings.TrimSpace(v)) {
			continue
		}
		n, _ := strconv.Atoi(v)
		news = append(news, uint64(n))
	}
	return news
}

// GetHost 获取主机硬件信息
func GetHost(agentConfig *model.AgentConfig) *model.Host {
	var ret model.Host

	var cpuType string
	hi, err := host.Info()
	if err != nil {
		println("host.Info error:", err)
	} else {
		if hi.VirtualizationSystem != "" {
			cpuType = "Virtual"
		} else {
			cpuType = "Physical"
		}
		ret.Platform = hi.Platform
		ret.PlatformVersion = hi.PlatformVersion
		ret.Arch = hi.KernelArch
		ret.Virtualization = hi.VirtualizationSystem
		ret.BootTime = hi.BootTime
	}

	cpuModelCount := make(map[string]int)
	ci, err := cpu.Info()
	if err != nil {
		println("cpu.Info error:", err)
	} else {
		for i := 0; i < len(ci); i++ {
			cpuModelCount[ci[i].ModelName]++
		}
		for model, count := range cpuModelCount {
			ret.CPU = append(ret.CPU, fmt.Sprintf("%s %d %s Core", model, count, cpuType))
		}
	}

	ret.DiskTotal, _ = getDiskTotalAndUsed(agentConfig)

	mv, err := mem.VirtualMemory()
	if err != nil {
		println("mem.VirtualMemory error:", err)
	} else {
		ret.MemTotal = mv.Total
		if runtime.GOOS != "windows" {
			ret.SwapTotal = mv.SwapTotal
		}
	}

	if runtime.GOOS == "windows" {
		ms, err := mem.SwapMemory()
		if err != nil {
			println("mem.SwapMemory error:", err)
		} else {
			ret.SwapTotal = ms.Total
		}
	}

	cachedBootTime = time.Unix(int64(hi.BootTime), 0)

	ret.IP = CachedIP
	ret.CountryCode = strings.ToLower(cachedCountry)

	//由于没办法重新生成GRPC文件,无法修改host和state
	//我们把Version拆分一下,中间用:连接,用来表示,例如0.14.6:0 后面的0表示没有有GPU,数字表示GPU的数量
	//获取GPU信息,看下是否有GPU,我们只能不可能去安装NVIDIA的DCGM来启动API,只能通过命令行指令去获取
	//lspci -vnn | grep VGA |grep -i nvi | wc -l返回的数字就是GPU的数量

	num := gpuHave()
	ret.Version = Version + "$" + strconv.Itoa(num)
	return &ret
}

func GetState(agentConfig *model.AgentConfig, skipConnectionCount bool, skipProcsCount bool) *model.HostState {
	var ret model.HostState

	cp, err := cpu.Percent(0, false)
	if err != nil {
		println("cpu.Percent error:", err)
	} else {
		ret.CPU = cp[0]
	}

	vm, err := mem.VirtualMemory()
	if err != nil {
		println("mem.VirtualMemory error:", err)
	} else {
		ret.MemUsed = vm.Total - vm.Available
		if runtime.GOOS != "windows" {
			ret.SwapUsed = vm.SwapTotal - vm.SwapFree
		}
	}
	if runtime.GOOS == "windows" {
		// gopsutil 在 Windows 下不能正确取 swap
		ms, err := mem.SwapMemory()
		if err != nil {
			println("mem.SwapMemory error:", err)
		} else {
			ret.SwapUsed = ms.Used
		}
	}

	_, ret.DiskUsed = getDiskTotalAndUsed(agentConfig)

	loadStat, err := load.Avg()
	if err != nil {
		println("load.Avg error:", err)
	} else {
		ret.Load1 = loadStat.Load1
		ret.Load5 = loadStat.Load5
		ret.Load15 = loadStat.Load15
	}

	var procs []int32
	if !skipProcsCount {
		procs, err = process.Pids()
		if err != nil {
			println("process.Pids error:", err)
		} else {
			ret.ProcessCount = uint64(len(procs))
		}
	}

	var tcpConnCount, udpConnCount uint64
	if !skipConnectionCount {
		ss_err := true
		if runtime.GOOS == "linux" {
			tcpStat, err_tcp := goss.ConnectionsWithProtocol(goss.AF_INET, syscall.IPPROTO_TCP)
			udpStat, err_udp := goss.ConnectionsWithProtocol(goss.AF_INET, syscall.IPPROTO_UDP)
			if err_tcp == nil && err_udp == nil {
				ss_err = false
				tcpConnCount = uint64(len(tcpStat))
				udpConnCount = uint64(len(udpStat))
			}
			if strings.Contains(CachedIP, ":") {
				tcpStat6, err_tcp := goss.ConnectionsWithProtocol(goss.AF_INET6, syscall.IPPROTO_TCP)
				udpStat6, err_udp := goss.ConnectionsWithProtocol(goss.AF_INET6, syscall.IPPROTO_UDP)
				if err_tcp == nil && err_udp == nil {
					ss_err = false
					tcpConnCount += uint64(len(tcpStat6))
					udpConnCount += uint64(len(udpStat6))
				}
			}
		}
		if ss_err {
			conns, _ := net.Connections("all")
			for i := 0; i < len(conns); i++ {
				switch conns[i].Type {
				case syscall.SOCK_STREAM:
					tcpConnCount++
				case syscall.SOCK_DGRAM:
					udpConnCount++
				}
			}
		}
	}

	ret.NetInTransfer, ret.NetOutTransfer = netInTransfer, netOutTransfer
	ret.NetInSpeed, ret.NetOutSpeed = netInSpeed, netOutSpeed
	ret.Uptime = uint64(time.Since(cachedBootTime).Seconds())
	//检查有没有GPU
	count := gpuHave()
	if tcpConnCount < 1 {
		tcpConnCount = 1
	}
	if udpConnCount < 1 {
		udpConnCount = 1
	}
	if count == 0 {
		ret.TcpConnCount, ret.UdpConnCount = tcpConnCount*1e9, udpConnCount*1e9
		return &ret
	}

	//这里我们把udpConnCount 和 TcpConnCount 这2个参数来传递多个参数,前提是used的这个切片里面的每个值都不大于100
	used := gpuUsed()

	switch len(used) {
	case 1:
		ret.TcpConnCount, ret.UdpConnCount = tcpConnCount*1e9+used[0], udpConnCount*1e9
	case 2:
		ret.TcpConnCount, ret.UdpConnCount = tcpConnCount*1e9+used[0], udpConnCount*1e9+used[1]
	case 3:
		one := used[0] * 1e6
		two := used[1] * 1e3
		three := used[2]
		ret.TcpConnCount = tcpConnCount*1e9 + one + two + three
		ret.UdpConnCount = udpConnCount * 1e9
	case 4:
		one := used[0] * 1e6
		two := used[1] * 1e3
		three := used[2]
		four := used[3] * 1e6
		ret.TcpConnCount = tcpConnCount*1e9 + one + two + three
		ret.UdpConnCount = udpConnCount*1e9 + four
	case 5:
		one := used[0] * 1e6
		two := used[1] * 1e3
		three := used[2]
		four := used[3] * 1e6
		five := used[4] * 1e3
		ret.TcpConnCount = tcpConnCount*1e9 + one + two + three
		ret.UdpConnCount = udpConnCount*1e9 + four + five
	case 6:
		one := used[0] * 1e6
		two := used[1] * 1e3
		three := used[2]
		four := used[3] * 1e6
		five := used[4] * 1e3
		six := used[5]
		ret.TcpConnCount = tcpConnCount*1e9 + one + two + three
		ret.UdpConnCount = udpConnCount*1e9 + four + five + six
	}
	return &ret
}

// TrackNetworkSpeed NIC监控，统计流量与速度
func TrackNetworkSpeed(agentConfig *model.AgentConfig) {
	var innerNetInTransfer, innerNetOutTransfer uint64
	nc, err := net.IOCounters(true)
	if err == nil {
		for _, v := range nc {
			if len(agentConfig.NICAllowlist) > 0 {
				if !agentConfig.NICAllowlist[v.Name] {
					continue
				}
			} else {
				if isListContainsStr(excludeNetInterfaces, v.Name) {
					continue
				}
			}
			innerNetInTransfer += v.BytesRecv
			innerNetOutTransfer += v.BytesSent
		}
		now := uint64(time.Now().Unix())
		diff := now - lastUpdateNetStats
		if diff > 0 {
			netInSpeed = (innerNetInTransfer - netInTransfer) / diff
			netOutSpeed = (innerNetOutTransfer - netOutTransfer) / diff
		}
		netInTransfer = innerNetInTransfer
		netOutTransfer = innerNetOutTransfer
		lastUpdateNetStats = now
	}
}

func getDiskTotalAndUsed(agentConfig *model.AgentConfig) (total uint64, used uint64) {
	devices := make(map[string]string)

	if len(agentConfig.HardDrivePartitionAllowlist) > 0 {
		// 如果配置了白名单，使用白名单的列表
		for i, v := range agentConfig.HardDrivePartitionAllowlist {
			devices[strconv.Itoa(i)] = v
		}
	} else {
		// 否则使用默认过滤规则
		diskList, _ := disk.Partitions(false)
		for _, d := range diskList {
			fsType := strings.ToLower(d.Fstype)
			// 不统计 K8s 的虚拟挂载点：https://github.com/shirou/gopsutil/issues/1007
			if devices[d.Device] == "" && isListContainsStr(expectDiskFsTypes, fsType) && !strings.Contains(d.Mountpoint, "/var/lib/kubelet") {
				devices[d.Device] = d.Mountpoint
			}
		}
	}

	for _, mountPath := range devices {
		diskUsageOf, err := disk.Usage(mountPath)
		if err == nil {
			total += diskUsageOf.Total
			used += diskUsageOf.Used
		}
	}

	// Fallback 到这个方法,仅统计根路径,适用于OpenVZ之类的.
	if runtime.GOOS == "linux" && total == 0 && used == 0 {
		cmd := exec.Command("df")
		out, err := cmd.CombinedOutput()
		if err == nil {
			s := strings.Split(string(out), "\n")
			for _, c := range s {
				info := strings.Fields(c)
				if len(info) == 6 {
					if info[5] == "/" {
						total, _ = strconv.ParseUint(info[1], 0, 64)
						used, _ = strconv.ParseUint(info[2], 0, 64)
						// 默认获取的是1K块为单位的.
						total = total * 1024
						used = used * 1024
					}
				}
			}
		}
	}

	return
}

func isListContainsStr(list []string, str string) bool {
	for i := 0; i < len(list); i++ {
		if strings.Contains(str, list[i]) {
			return true
		}
	}
	return false
}

func println(v ...interface{}) {
	fmt.Printf("NEZHA@%s>> ", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Println(v...)
}

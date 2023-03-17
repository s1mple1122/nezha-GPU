package main

import (
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

func main() {
	s := gpuUsed()
	fmt.Println("----------------------------")
	fmt.Println(s)
	fmt.Println(len(s))
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
	fmt.Println("cli执行结果 = ", string(bytes))
	s := strings.Split(string(bytes), `%`)
	fmt.Println(s)
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

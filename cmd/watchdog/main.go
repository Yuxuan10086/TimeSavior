package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"timesavior/internal/config"
	"timesavior/internal/guard"
	"timesavior/internal/sysproxy"
)

func main() {
	cfgPath := flag.String("config", "configs/default.yaml", "config path")
	corePath := flag.String("core", "", "path to timesavior.exe (default: alongside watchdog)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	host, port, err := cfg.ListenHostPort()
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	absCfg, _ := filepath.Abs(*cfgPath)
	exe := *corePath
	if exe == "" {
		exe = filepath.Join(guard.SelfDir(), "timesavior.exe")
		if _, err := os.Stat(exe); err != nil {
			exe = filepath.Join(guard.SelfDir(), "timesavior")
		}
	}
	exe, _ = filepath.Abs(exe)

	iv := time.Duration(cfg.Watchdog.CheckSeconds) * time.Second
	if iv <= 0 {
		iv = time.Second
	}
	log.Printf("watchdog started; core=%s proxy=%s:%d", exe, host, port)

	for {
		coreName := filepath.Base(exe)
		if !portOpen(host, port) || !guard.ExeRunning(coreName) {
			log.Printf("core missing/not listening, starting %s", exe)
			_ = guard.StartDetached(exe, "-config", absCfg)
			time.Sleep(1500 * time.Millisecond)
		}
		rewrote, from, err := sysproxy.Ensure(host, port)
		if err == nil && rewrote {
			log.Printf("sysproxy rewritten from %s", from)
		}
		time.Sleep(iv)
	}
}

func portOpen(host string, port int) bool {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	conn, err := net.DialTimeout("tcp", addr, 400*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

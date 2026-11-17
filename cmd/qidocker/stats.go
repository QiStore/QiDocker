package main

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func cpu() (total, idle uint64) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return
	}
	line, _, _ := strings.Cut(string(data), "\n")
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return
	}
	for i, value := range fields[1:min(len(fields), 9)] {
		count, _ := strconv.ParseUint(value, 10, 64)
		total += count
		if i == 3 || i == 4 {
			idle += count
		}
	}
	return
}

func systemStats(w http.ResponseWriter) {
	total1, idle1 := cpu()
	time.Sleep(150 * time.Millisecond)
	total2, idle2 := cpu()
	percent := 0.0
	if total2 > total1 && idle2 >= idle1 {
		percent = 100 * (1 - float64(idle2-idle1)/float64(total2-total1))
	}
	memory := make(map[string]uint64)
	data, _ := os.ReadFile("/proc/meminfo")
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		memory[fields[0]] = value * 1024
	}
	total := memory["MemTotal:"]
	available := memory["MemAvailable:"]
	if _, ok := memory["MemAvailable:"]; !ok {
		available = memory["MemFree:"] + memory["Buffers:"] + memory["Cached:"]
	}
	writeJSON(w, map[string]any{"cpu": percent, "memory_total": total, "memory_used": total - min(available, total)})
}

// 只建立自身回环连接，帮助定位 CI Seatbelt 拒绝；不是业务验收或重试机制。
package main

import (
	"encoding/json"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type result struct {
	Network  string         `json:"network"`
	Address  string         `json:"address"`
	Success  int64          `json:"success"`
	Failures map[string]int `json:"failures"`
}

func main() {
	results := make([]result, 0, 3)
	for _, config := range [][2]string{{"tcp4", "127.0.0.1:0"}, {"tcp6", "[::1]:0"}, {"tcp", "localhost:0"}} {
		row := result{Network: config[0], Address: config[1], Failures: make(map[string]int)}
		listener, err := net.Listen(config[0], config[1])
		if err != nil {
			row.Failures[err.Error()]++
			results = append(results, row)
			continue
		}
		row.Address = listener.Addr().String()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				connection, acceptErr := listener.Accept()
				if acceptErr != nil {
					return
				}
				_ = connection.Close()
			}
		}()
		var success atomic.Int64
		var mu sync.Mutex
		var group sync.WaitGroup
		for range 8 {
			group.Go(func() {
				for range 250 {
					connection, dialErr := net.DialTimeout(config[0], row.Address, time.Second)
					if dialErr != nil {
						mu.Lock()
						row.Failures[dialErr.Error()]++
						mu.Unlock()
						continue
					}
					success.Add(1)
					_ = connection.Close()
				}
			})
		}
		group.Wait()
		_ = listener.Close()
		<-done
		row.Success = success.Load()
		results = append(results, row)
	}
	if err := json.NewEncoder(os.Stdout).Encode(results); err != nil {
		panic(err)
	}
}

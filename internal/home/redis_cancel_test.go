package home

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClusterLatencyProbeCancellationInterruptsBlockedPing(t *testing.T) {
	for _, blockedPing := range []int32{1, 2} {
		name := "warmup"
		if blockedPing == 2 {
			name = "measurement"
		}
		t.Run(name, func(t *testing.T) {
			blocked := make(chan struct{})
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			var pings atomic.Int32
			client, _ := newRedisCommandTestClient(t, func(args []string) string {
				if strings.EqualFold(args[0], "PING") && pings.Add(1) == blockedPing {
					close(blocked)
					<-release
				}
				return balanceTestRedisReply(args)
			})
			address := clusterNodeAddress(clusterNode{IP: client.homeCfg.Host, Port: client.homeCfg.Port})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, errProbe := client.probeClusterLatency(ctx, address)
				done <- errProbe
			}()
			select {
			case <-blocked:
			case errProbe := <-done:
				t.Fatalf("probe ended before blocked PING: %v", errProbe)
			case <-time.After(2 * time.Second):
				t.Fatal("probe did not start")
			}
			cancel()
			select {
			case errProbe := <-done:
				if errProbe == nil {
					t.Fatal("canceled probe reported success")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation did not interrupt the blocked PING")
			}
		})
	}
}

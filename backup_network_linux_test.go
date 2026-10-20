//go:build linux

package main

import (
	"context"
	"net"
	"os"
	"testing"
	"time"
)

// Enabled only by the isolated Docker verification runner. The namespace must
// contain no non-loopback interface, independently of HTTP/provider test doubles.
func TestBackupLinuxNetworkNamespace(t *testing.T) {
	if os.Getenv("YOLINK_OS_NETWORK_ISOLATED") != "1" {
		t.Skip("requires isolated Docker runner")
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		addresses, e := iface.Addrs()
		if e != nil {
			t.Fatal(e)
		}
		t.Logf("namespace interface: %s flags=%s addresses=%v", iface.Name, iface.Flags, addresses)
		// Some NAS kernels expose a dormant, unconfigured sit0 tunnel even in
		// Docker network=none. It has neither UP nor an address/usable route.
		if iface.Flags&net.FlagLoopback == 0 && (iface.Flags&net.FlagUp != 0 || len(addresses) != 0) {
			t.Fatal("non-loopback network interface in restore drill", iface.Name)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", "198.18.0.1:443")
	if err == nil {
		conn.Close()
		t.Fatal("restore namespace permitted outbound connection")
	}
}

// Copyright 2024 qbee.io
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"testing"
	"time"
)

func Test_UDPTunnel(t *testing.T) {
	testConnTTL := 5 * time.Second
	// start two tests targets, we want to simulate TFTP scenario,
	// where initial request is sent to a known service port,
	// but the actual data transfer is done via a random port.
	// In our case, we want to make sure that data coming from a different remote port,
	// is also forwarded through a different local port.
	// The expected conversation is as follows:
	// [local>primary]   -> "syn" -> [remote>primary]
	// [local<secondary] <- "ack" <- [remote<secondary]
	// [local>secondary] -> "ack" -> [remote>secondary]
	// [local>primary]   -> "fin" -> [remote>primary]
	// [local<primary]   <- "fin" <- [remote<primary]
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	remotePrimaryListener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: 3001})
	if err != nil {
		t.Fatal(err)
	}

	_ = remotePrimaryListener.SetDeadline(time.Now().Add(testConnTTL))

	remotePrimaryHostPort := fmt.Sprintf("localhost:%d", remotePrimaryListener.LocalAddr().(*net.UDPAddr).Port)

	var remoteSecondaryListener *net.UDPConn
	remoteSecondaryListener, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: 3002})

	_ = remoteSecondaryListener.SetDeadline(time.Now().Add(testConnTTL))

	t.Log("primary remote:", remotePrimaryListener.LocalAddr().String())
	t.Log("secondary remote:", remoteSecondaryListener.LocalAddr().String())

	go func(t *testing.T) {
		buf := make([]byte, 3)
		t.Log("dev: waiting for syn on primary dst port", remotePrimaryListener.LocalAddr().String())
		_, addr, goErr := remotePrimaryListener.ReadFromUDP(buf)
		if goErr != nil {
			t.Error(goErr)
			return
		}

		t.Log("dev: checking if we got 'syn' on primary dst port")
		if string(buf) != "syn" {
			t.Errorf("expected 'syn', got '%s'", string(buf))
			return
		}

		t.Log("dev: sending a response from the secondary port ->", addr.String())
		if _, goErr = remoteSecondaryListener.WriteToUDP([]byte("ack"), addr); goErr != nil {
			t.Error(goErr)
			return
		}

		t.Log("dev: reading ack from secondary port", remoteSecondaryListener.LocalAddr().String())
		if _, _, goErr = remoteSecondaryListener.ReadFromUDP(buf); goErr != nil {
			t.Error(goErr)
			return
		}

		t.Log("dev: checking if we got 'ack' from secondary port")
		if string(buf) != "ack" {
			t.Errorf("expected 'ack', got '%s'", string(buf))
			return
		}

		t.Log("dev: reading fin on the primary port", remotePrimaryListener.LocalAddr().String())
		if _, addr, goErr = remotePrimaryListener.ReadFromUDP(buf); goErr != nil {
			t.Error(goErr)
			return
		}

		t.Log("dev: checking if we got 'fin' on primary dst port")
		if string(buf) != "fin" {
			t.Errorf("expected 'fin', got '%s'", string(buf))
			return
		}

		t.Log("dev: sending a response from the primary port ->", addr.String())
		if _, err = remotePrimaryListener.WriteToUDP([]byte("fin"), addr); err != nil {
			t.Error(err)
			return
		}

		t.Log("dev: done")
	}(t)

	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeUDPTunnel, HandleUDPTunnel)

	localHostPort := "127.0.0.1:2001"

	t.Log("cli: opening tunnel")
	var udpTunnel *UDPTunnel
	udpTunnel, err = client.OpenUDPTunnel(ctx, localHostPort, remotePrimaryHostPort)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udpTunnel.Close() })

	t.Log("cli: initializing listener")
	var clientListener *net.UDPConn
	if clientListener, err = net.ListenUDP("udp", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: 2000}); err != nil {
		t.Fatal(err)
	}
	t.Log("cli: listener", clientListener.LocalAddr().String())
	_ = clientListener.SetDeadline(time.Now().Add(testConnTTL))

	t.Log("cli: sending syn to primary dst port ->", udpTunnel.PrimaryAddr().String())
	if _, err = clientListener.WriteToUDP([]byte("syn"), udpTunnel.PrimaryAddr()); err != nil {
		t.Fatal(err)
	}

	t.Log("cli: waiting for ack from secondary dst port on", clientListener.LocalAddr().String())
	buf := make([]byte, 3)
	var addr *net.UDPAddr
	if _, addr, err = clientListener.ReadFromUDP(buf); err != nil {
		t.Fatal(err)
	}

	t.Log("cli: checking if we got 'ack' from secondary dst port")
	if string(buf) != "ack" {
		t.Errorf("expected 'ack', got '%s'", string(buf))
		return
	}

	t.Log("cli: making sure we got the ack from secondary port", addr.Port, udpTunnel.PrimaryAddr().Port)
	if addr.Port == udpTunnel.PrimaryAddr().Port {
		t.Errorf("expected addr to have a different port than udpTunnel.PrimaryAddr(), got %d", addr.Port)
		return
	}

	t.Log("cli: sending ack response back to secondary dst port ->", addr.String())
	if _, err = clientListener.WriteToUDP([]byte("ack"), addr); err != nil {
		t.Fatal(err)
	}

	t.Log("cli: sending fin to primary dst port ->", udpTunnel.PrimaryAddr().String())
	if _, err = clientListener.WriteToUDP([]byte("fin"), udpTunnel.PrimaryAddr()); err != nil {
		t.Fatal(err)
	}

	t.Log("cli: waiting for fin from primary port on", clientListener.LocalAddr().String())
	if _, addr, err = clientListener.ReadFromUDP(buf); err != nil {
		t.Fatal(err)
	}

	t.Log("cli: checking if we got 'fin' from primary port")
	if string(buf) != "fin" {
		t.Errorf("expected 'fin', got '%s'", string(buf))
		return
	}

	t.Log("cli: making sure we got the fin from primary port", addr.Port, udpTunnel.PrimaryAddr().Port)
	// verify that addr has the same port as udpTunnel.PrimaryAddr()
	if addr.Port != udpTunnel.PrimaryAddr().Port {
		t.Errorf("expected addr to have the same port as udpTunnel.PrimaryAddr(), got %d", addr.Port)
		return
	}

	t.Log("cli: done")
}

func Test_newDeviceUDPListener(t *testing.T) {
	t.Run("ipv4-localhost", func(t *testing.T) {
		dstAddr := &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: 3001}
		suggestedPort := "2001"
		expectedLocalAddr := "127.0.0.1:2001"

		// for the first listener, we expect the suggested port to be available and used
		listener, err := newDeviceUDPListener(dstAddr, suggestedPort)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })

		if listener.LocalAddr().String() != expectedLocalAddr {
			t.Errorf("expected %s, got %s", expectedLocalAddr, listener.LocalAddr())
		}

		// for the second listener, we expect the suggested port to be unavailable and a random port to be used
		var listener2 *net.UDPConn
		if listener2, err = newDeviceUDPListener(dstAddr, suggestedPort); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener2.Close() })

		if listener2.LocalAddr().String() == expectedLocalAddr {
			t.Errorf("expected address to be different than %s, got %s", expectedLocalAddr, listener.LocalAddr())
		}
	})

	t.Run("ipv4-remote", func(t *testing.T) {
		dstAddr := &net.UDPAddr{IP: net.IP{192, 168, 0, 1}, Port: 3001}
		suggestedPort := "2001"

		// for the first listener, we expect the suggested port to be available and used
		listener, err := newDeviceUDPListener(dstAddr, suggestedPort)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })

		localAddr := listener.LocalAddr().(*net.UDPAddr)
		if !localAddr.IP.IsUnspecified() {
			t.Errorf("expected IP 0.0.0.0, got %s", localAddr.IP.String())
		}
		if localAddr.Port != 2001 {
			t.Errorf("expected port %d, got %d", 2001, localAddr.Port)
		}

		// for the second listener, we expect the suggested port to be unavailable and a random port to be used
		var listener2 *net.UDPConn
		if listener2, err = newDeviceUDPListener(dstAddr, suggestedPort); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener2.Close() })

		localAddr = listener2.LocalAddr().(*net.UDPAddr)
		if !localAddr.IP.IsUnspecified() {
			t.Errorf("expected IP 0.0.0.0, got %s", localAddr.IP.String())
		}
		if localAddr.Port == 2001 {
			t.Errorf("expected port different than %d, got %d", 2001, localAddr.Port)
		}
	})

	t.Run("ipv6-localhost", func(t *testing.T) {
		dstAddr := &net.UDPAddr{IP: net.IPv6loopback, Port: 3001}
		suggestedPort := "2001"
		expectedLocalAddr := "[::1]:2001"

		// for the first listener, we expect the suggested port to be available and used
		listener, err := newDeviceUDPListener(dstAddr, suggestedPort)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })

		if listener.LocalAddr().String() != expectedLocalAddr {
			t.Errorf("expected %s, got %s", expectedLocalAddr, listener.LocalAddr())
		}

		// for the second listener, we expect the suggested port to be unavailable and a random port to be used
		var listener2 *net.UDPConn
		if listener2, err = newDeviceUDPListener(dstAddr, suggestedPort); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener2.Close() })

		if listener2.LocalAddr().String() == expectedLocalAddr {
			t.Errorf("expected address to be different than %s, got %s", expectedLocalAddr, listener.LocalAddr())
		}
	})

	t.Run("ipv6-remote", func(t *testing.T) {
		dstAddr := &net.UDPAddr{IP: net.IPv6linklocalallnodes, Port: 3001}
		suggestedPort := "2001"
		expectedLocalAddr := "[::]:2001"

		// for the first listener, we expect the suggested port to be available and used
		listener, err := newDeviceUDPListener(dstAddr, suggestedPort)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener.Close() })

		if listener.LocalAddr().String() != expectedLocalAddr {
			t.Errorf("expected %s, got %s", expectedLocalAddr, listener.LocalAddr())
		}

		// for the second listener, we expect the suggested port to be unavailable and a random port to be used
		var listener2 *net.UDPConn
		if listener2, err = newDeviceUDPListener(dstAddr, suggestedPort); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = listener2.Close() })

		if listener2.LocalAddr().String() == expectedLocalAddr {
			t.Errorf("expected address to be different than %s, got %s", expectedLocalAddr, listener.LocalAddr())
		}
	})
}

func Test_isExpectedUDPSource(t *testing.T) {
	t.Run("accept-same-ip-different-port", func(t *testing.T) {
		dstAddr := &net.UDPAddr{IP: net.IP{192, 168, 1, 10}, Port: 53}
		srcAddr := &net.UDPAddr{IP: net.IP{192, 168, 1, 10}, Port: 32123}

		if !isExpectedUDPSource(srcAddr, dstAddr) {
			t.Fatal("expected source to be accepted")
		}
	})

	t.Run("reject-different-ip", func(t *testing.T) {
		dstAddr := &net.UDPAddr{IP: net.IP{192, 168, 1, 10}, Port: 53}
		srcAddr := &net.UDPAddr{IP: net.IP{192, 168, 1, 11}, Port: 53}

		if isExpectedUDPSource(srcAddr, dstAddr) {
			t.Fatal("expected source to be rejected")
		}
	})

	t.Run("reject-zone-mismatch-for-link-local", func(t *testing.T) {
		dstAddr := &net.UDPAddr{IP: net.ParseIP("fe80::1"), Port: 53, Zone: "en0"}
		srcAddr := &net.UDPAddr{IP: net.ParseIP("fe80::1"), Port: 5353, Zone: "en1"}

		if isExpectedUDPSource(srcAddr, dstAddr) {
			t.Fatal("expected source to be rejected")
		}
	})

	t.Run("reject-nil-addresses", func(t *testing.T) {
		dstAddr := &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: 53}

		if isExpectedUDPSource(nil, dstAddr) {
			t.Fatal("expected nil source to be rejected")
		}

		srcAddr := &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: 12345}
		if isExpectedUDPSource(srcAddr, nil) {
			t.Fatal("expected nil destination to be rejected")
		}
	})
}

// Test_UDPTunnel_SourceIPInjection is an end-to-end test that verifies the device side of a UDP
// tunnel refuses to forward datagrams originating from a host other than the configured destination.
//
// Scenario: a client on host A opens a tunnel to a service on host B, while an attacker on host C
// injects a spoofed datagram directly at the device's local listener. The device must drop the
// injected datagram so it never reaches host A. While the device-side source-IP validation is
// disabled, the injected datagram leaks through and this test fails - which is the point.
//
// The attacker socket is bound to 127.0.0.2. The full 127.0.0.0/8 loopback range is routable out of
// the box only on Linux (macOS needs "sudo ifconfig lo0 alias 127.0.0.2 up"), so the test is
// restricted to Linux and skipped elsewhere.
func Test_UDPTunnel_SourceIPInjection(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("this test binds an attacker socket to 127.0.0.2, which is only reliably available on Linux.\n" +
			"Run it inside a Linux container, e.g.:\n" +
			"  docker run --rm -v \"$PWD\":/src -w /src golang:1.24 go test -run Test_UDPTunnel_SourceIPInjection ./...")
	}

	const testConnTTL = 5 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// host B: the legitimate remote service the client wants to reach.
	remoteServer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = remoteServer.Close() })
	_ = remoteServer.SetDeadline(time.Now().Add(testConnTTL))

	remoteHostPort := fmt.Sprintf("127.0.0.1:%d", remoteServer.LocalAddr().(*net.UDPAddr).Port)
	t.Log("host B (legit remote):", remoteHostPort)

	// wire up the edge together with a client and a device, and register the UDP tunnel handler.
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeUDPTunnel, HandleUDPTunnel)

	// open the tunnel: local primary listener -> device -> host B.
	udpTunnel, err := client.OpenUDPTunnel(ctx, "127.0.0.1:0", remoteHostPort)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = udpTunnel.Close() })
	t.Log("tunnel primary listener:", udpTunnel.PrimaryAddr().String())

	// host A: the local client that drives the conversation.
	clientListener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IP{127, 0, 0, 1}, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientListener.Close() })
	t.Log("host A (client):", clientListener.LocalAddr().String())

	// host A sends the first datagram, which makes the device open its local listener and forward it
	// to host B.
	_ = clientListener.SetWriteDeadline(time.Now().Add(testConnTTL))
	if _, err = clientListener.WriteToUDP([]byte("hello"), udpTunnel.PrimaryAddr()); err != nil {
		t.Fatal(err)
	}

	// host B receives the datagram and learns the device's local listener address.
	buf := make([]byte, maxUDPPacketSize)
	n, deviceAddr, err := remoteServer.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != "hello" {
		t.Fatalf("host B expected \"hello\", got %q", got)
	}
	t.Log("host B: received \"hello\" from device listener", deviceAddr.String())

	// sanity check: a legitimate reply from host B must reach host A through the tunnel. This proves
	// the tunnel plumbing works, so a missing injected packet later can only be due to source filtering.
	if _, err = remoteServer.WriteToUDP([]byte("world"), deviceAddr); err != nil {
		t.Fatal(err)
	}
	_ = clientListener.SetReadDeadline(time.Now().Add(testConnTTL))
	if n, _, err = clientListener.ReadFromUDP(buf); err != nil {
		t.Fatalf("host A did not receive the legitimate reply from host B: %v", err)
	}
	if got := string(buf[:n]); got != "world" {
		t.Fatalf("host A expected legitimate \"world\", got %q", got)
	}
	t.Log("host A: received legitimate \"world\"")

	// host C (attacker): bind to a different loopback source IP and inject a datagram directly at the
	// device's local listener, spoofing a reply that appears to come from host B.
	attacker, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IP{127, 0, 0, 2}, Port: 0})
	if err != nil {
		t.Fatalf("failed to bind attacker socket on 127.0.0.2: %v", err)
	}
	t.Cleanup(func() { _ = attacker.Close() })
	t.Log("host C (attacker):", attacker.LocalAddr().String())

	_ = attacker.SetWriteDeadline(time.Now().Add(testConnTTL))
	if _, err = attacker.WriteToUDP([]byte("INJECTED"), deviceAddr); err != nil {
		t.Fatal(err)
	}
	t.Log("host C: injected spoofed datagram at device listener", deviceAddr.String())

	// the device must drop the spoofed datagram, so host A must never receive it.
	_ = clientListener.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from, err := clientListener.ReadFromUDP(buf)
	if err == nil {
		t.Fatalf("security regression: host A received injected packet %q from %s; "+
			"the device forwarded a datagram from an unexpected source IP", buf[:n], from)
	}
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("unexpected error while confirming the injected packet was dropped: %v", err)
	}

	t.Log("host A: injected packet correctly dropped by the device")
}

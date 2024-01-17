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
	"fmt"
	"net"
	"testing"
	"time"
)

func Test_UDPTunnel(t *testing.T) {
	testConnTTL := 5 * time.Minute
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
		if _, addr, goErr = remoteSecondaryListener.ReadFromUDP(buf); goErr != nil {
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

	client, _, _ := NewEdgeMock(t)

	localHostPort := "127.0.0.1:2001"

	t.Log("cli: opening tunnel")
	var udpTunnel *UDPTunnel
	udpTunnel, err = client.OpenUDPTunnel(ctx, localHostPort, remotePrimaryHostPort)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(udpTunnel.Close)

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

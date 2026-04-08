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
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func Test_TCPTunnel(t *testing.T) {
	// test the following path
	// [http client] -> [udpConn@client] -> [edge] -> [remoteConnection@device] -> [remote HTTP server]

	// set up a test HTTP server we want to tunnel to
	requested := false
	remoteHTTPServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("hello world"))
	}))

	// set up a mock edge infrastructure
	client, deviceClient, _ := NewEdgeMock(t)
	deviceClient.WithHandler(MessageTypeTCPTunnel, HandleTCPTunnel)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// open a TCP tunnel to the test HTTP server
	localListener, err := client.OpenTCPTunnel(ctx, "localhost:0", remoteHTTPServer.Listener.Addr().String())
	if err != nil {
		t.Fatalf("error opening TCP tunnel: %v", err)
	}
	t.Cleanup(func() { _ = localListener.Close() })

	testURL := "http://" + localListener.Addr().String()

	// request our demo server via the forwarded port
	ctxWithTimeout, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()

	var httpRequest *http.Request
	if httpRequest, err = http.NewRequestWithContext(ctxWithTimeout, http.MethodGet, testURL, nil); err != nil {
		t.Fatalf("error creating HTTP request: %v", err)
	}

	var httpResponse *http.Response
	httpResponse, err = http.DefaultClient.Do(httpRequest)
	if err != nil {
		t.Fatalf("error sending HTTP request: %v", err)
	}

	// check that the request was successful
	if httpResponse.StatusCode != http.StatusOK {
		t.Fatalf("unexpected HTTP status code: %v", httpResponse.StatusCode)
	}

	// check that the request was received by the demo server
	if !requested {
		t.Fatalf("request not received by the demo server")
	}
}

func BenchmarkTCPTunnel(b *testing.B) {
	client, deviceClient, _ := NewEdgeMock(b)
	deviceClient.WithHandler(MessageTypeTCPTunnel, HandleTCPTunnel)

	ctx, cancel := context.WithCancel(context.Background())
	b.Cleanup(cancel)

	remoteListener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		b.Fatalf("error opening remote listener: %v", err)
	}
	b.Cleanup(func() { _ = remoteListener.Close() })

	var localListener net.Listener
	if localListener, err = client.OpenTCPTunnel(ctx, "localhost:0", remoteListener.Addr().String()); err != nil {
		b.Fatalf("error opening TCP tunnel: %v", err)
	}
	b.Cleanup(func() { _ = localListener.Close() })

	var localConn, remoteConn net.Conn

	var localConnErr error
	go func() {
		localConn, localConnErr = net.Dial("tcp", localListener.Addr().String())
	}()

	if remoteConn, err = remoteListener.Accept(); err != nil {
		b.Fatalf("error accepting remote connection: %v", err)
	}
	b.Cleanup(func() { _ = remoteConn.Close() })

	if localConnErr != nil {
		b.Fatalf("error opening local connection: %v", err)
	}
	b.Cleanup(func() { _ = localConn.Close() })

	const bufSize = 128 * 1024
	buf := make([]byte, bufSize)
	buf2 := make([]byte, bufSize)
	b.SetBytes(bufSize)
	b.ResetTimer()
	b.ReportAllocs()

	errCh := make(chan error)

	go func() {
		var n int
		var readErr error
		for bytesRemaining := b.N * bufSize; bytesRemaining > 0; bytesRemaining -= n {
			if n, readErr = remoteConn.Read(buf2); readErr != nil {
				errCh <- readErr
				return
			}
		}

		errCh <- nil
	}()
	for i := 0; i < b.N; i++ {
		if _, err = localConn.Write(buf); err != nil {
			b.Fatalf("error writing to local connection: %v", err)
		}
	}

	if err = <-errCh; err != nil {
		b.Fatalf("error reading from remote connection: %v", err)
	}
}

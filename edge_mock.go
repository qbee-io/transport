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
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"

	"github.com/xtaci/smux"
)

type Tester interface {
	Errorf(format string, args ...interface{})
	Fatalf(format string, args ...interface{})
	Cleanup(action func())
}

// EdgeMock provides a minimalistic edge server implementation to make testing of clients a bit easier.
type EdgeMock struct {
	t          Tester
	device     *smux.Session
	httpServer *httptest.Server
}

// ServeHTTP connects two clients together.
func (edge *EdgeMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	isDevice := r.Header.Get("Authorization") == ""

	if !isDevice && edge.device == nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("device not connected"))
		return
	}

	if isDevice && edge.device != nil {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte("device already connected"))
		return
	}

	smuxSession, err := UpgradeHandler(w, r)
	if err != nil {
		return
	}
	defer smuxSession.Close()

	if isDevice {
		edge.device = smuxSession
	}

	for {
		var stream *smux.Stream
		if stream, err = smuxSession.AcceptStream(); err != nil {
			return
		}

		// don't accept any streams from the device
		if isDevice {
			_ = stream.Close()
			edge.t.Errorf("got a stream from device - that's not allowed")
			continue
		}

		var deviceStream *smux.Stream
		if deviceStream, err = edge.device.OpenStream(); err != nil {
			_ = stream.Close()
			edge.t.Errorf("error opening device stream: %v", err)
			continue
		}

		if _, _, err = Pipe(stream, deviceStream); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}

			edge.t.Errorf("error in pipe: %v", err)
		}
	}
}

// URL returns the URL of the test server.
func (edge *EdgeMock) URL() string {
	return edge.httpServer.URL
}

// ClientTLS returns the TLS configuration of the test server.
func (edge *EdgeMock) ClientTLS() *tls.Config {
	caCertPool := x509.NewCertPool()
	caCertPool.AddCert(edge.httpServer.Certificate())

	return &tls.Config{
		ServerName: "example.com",
		RootCAs:    caCertPool,
	}
}

// DeviceClient returns a new device client connected to the test server.
func (edge *EdgeMock) DeviceClient() *DeviceClient {
	deviceClient, err := NewDeviceClient(edge.httpServer.URL, edge.ClientTLS())
	if err != nil {
		edge.t.Fatalf("failed to create device client: %v", err)
	}
	deviceClient.Start(context.Background())

	edge.t.Cleanup(func() { _ = deviceClient.Close() })
	deviceClient.Ready()

	return deviceClient
}

// Client returns a new client connected to the test server.
func (edge *EdgeMock) Client() *Client {
	client, err := NewClient(context.Background(), edge.httpServer.URL, "client", edge.ClientTLS())
	if err != nil {
		edge.t.Fatalf("failed to create client: %v", err)
	}

	edge.t.Cleanup(func() { _ = client.Close() })

	return client
}

// NewEdgeMock creates a new test server and returns it together with a client and a device client connected to it.
func NewEdgeMock(t Tester) (*Client, *DeviceClient, *EdgeMock) {
	edgeMock := &EdgeMock{t: t}
	edgeMock.httpServer = httptest.NewTLSServer(edgeMock)
	t.Cleanup(edgeMock.httpServer.Close)

	deviceClient := edgeMock.DeviceClient()
	client := edgeMock.Client()

	return client, deviceClient, edgeMock
}

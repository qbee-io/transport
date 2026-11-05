// Copyright 2026 qbee.io
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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDeviceClient_Callbacks(t *testing.T) {
	var failing atomic.Bool
	failing.Store(true)

	edge := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}

		session, err := UpgradeHandler(w, r)
		if err != nil {
			return
		}
		<-session.CloseChan()
	}))
	t.Cleanup(edge.Close)

	caCertPool := x509.NewCertPool()
	caCertPool.AddCert(edge.Certificate())
	tlsConfig := &tls.Config{ServerName: "example.com", RootCAs: caCertPool}

	errCh := make(chan error, 10)
	connectCh := make(chan struct{}, 10)

	deviceClient, err := NewDeviceClient(edge.URL, tlsConfig)
	if err != nil {
		t.Fatalf("failed to create device client: %v", err)
	}

	deviceClient.
		WithErrorHandler(func(err error) { errCh <- err }).
		WithConnectHandler(func() { connectCh <- struct{}{} })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	deviceClient.Start(ctx)
	t.Cleanup(func() { _ = deviceClient.Close() })

	select {
	case err = <-errCh:
		if !strings.Contains(err.Error(), "503") {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-connectCh:
		t.Fatalf("unexpected connect")
	case <-time.After(5 * time.Second):
		t.Fatalf("error handler not called")
	}

	failing.Store(false)

	select {
	case <-connectCh:
	case <-time.After(15 * time.Second):
		t.Fatalf("connect handler not called")
	}

	deviceClient.Ready()
}

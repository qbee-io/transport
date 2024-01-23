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
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/xtaci/smux"
)

// ioWaitTimeout is the timeout for I/O operations.
// We use a timeout to avoid blocking the tunnel indefinitely.
const ioWaitTimeout = 15 * time.Second

var udpIOWaitTimeoutCtxKey = struct{}{}

// WithIOWaitTimeout returns a copy of the context with the specified timeout for I/O operations.
func WithIOWaitTimeout(ctx context.Context, timeout time.Duration) context.Context {
	return context.WithValue(ctx, udpIOWaitTimeoutCtxKey, timeout)
}

// getIOWaitTimeout returns the timeout for I/O operations.
// If the context contains a timeout, that timeout will be returned.
// Otherwise, the default timeout will be returned.
func getIOWaitTimeout(ctx context.Context) time.Duration {
	if timeout, ok := ctx.Value(udpIOWaitTimeoutCtxKey).(time.Duration); ok {
		return timeout
	}

	return ioWaitTimeout
}

// Protocol is the protocol to which the localListener is upgraded.
const Protocol = "qbee-v1"

// DefaultSmuxConfig is the default smux configuration.
var DefaultSmuxConfig = &smux.Config{
	Version:           2,
	KeepAliveInterval: 45 * time.Second,
	KeepAliveTimeout:  120 * time.Second,
	MaxFrameSize:      32768,
	MaxReceiveBuffer:  4194304,
	MaxStreamBuffer:   65536,
}

const protocolUpgradeResponse = "HTTP/1.1 101 Switching Protocols\r\n" +
	"Connection: Upgrade\r\n" +
	"Upgrade: " + Protocol + "\r\n" +
	"\r\n"

// UpgradeHandler upgrades the server request localListener to the remote access protocol.
// For protocol errors, the handler will write an error response and return an error.
func UpgradeHandler(w http.ResponseWriter, r *http.Request) (*smux.Session, error) {
	if r.Header.Get("Connection") != "upgrade" || r.Header.Get("Upgrade") != Protocol {
		w.WriteHeader(http.StatusUpgradeRequired)
		return nil, fmt.Errorf("upgrade required")
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error"))
		return nil, fmt.Errorf("response does not implement http.Hijacker")
	}

	netConn, _, err := hijacker.Hijack()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error"))
		return nil, fmt.Errorf("failed to hijack localListener: %w", err)
	}

	if _, err = netConn.Write([]byte(protocolUpgradeResponse)); err != nil {
		_ = netConn.Close()
		return nil, fmt.Errorf("failed to write response: %w", err)
	}

	return smux.Server(netConn, DefaultSmuxConfig)
}

var dialer = net.Dialer{
	Timeout: 30 * time.Second,
}

// ClientConnect initiates smux session with the provided edge endpoint.
func ClientConnect(ctx context.Context, endpointURL, authHeader string, tlsConfig *tls.Config) (*smux.Session, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, endpointURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	if authHeader != "" {
		httpRequest.Header.Set("Authorization", authHeader)
	}

	httpRequest.Header.Set("Connection", "upgrade")
	httpRequest.Header.Set("Upgrade", Protocol)

	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         dialer.DialContext,
			DisableKeepAlives:   true,
			TLSHandshakeTimeout: 10 * time.Second,
			TLSClientConfig:     tlsConfig,
		},
	}

	var httpResponse *http.Response
	if httpResponse, err = httpClient.Do(httpRequest); err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if httpResponse.StatusCode != http.StatusSwitchingProtocols {
		_ = httpResponse.Body.Close()
		return nil, fmt.Errorf("failed to upgrade, got %d", httpResponse.StatusCode)
	}

	var smuxSession *smux.Session
	if smuxSession, err = smux.Client(httpResponse.Body.(io.ReadWriteCloser), DefaultSmuxConfig); err != nil {
		_ = httpResponse.Body.Close()
		return nil, fmt.Errorf("failed to create smux session: %w", err)
	}

	return smuxSession, nil
}

// OpenStream opens a new stream and sends the given message type and payload.
func OpenStream(ctx context.Context, session *smux.Session, msgType MessageType, payload []byte) (*smux.Stream, error) {
	ioWaitTimeoutDuration := getIOWaitTimeout(ctx)

	_ = session.SetDeadline(time.Now().Add(ioWaitTimeoutDuration))

	stream, err := session.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("error opening stream: %v", err)
	}

	if err = WriteMessage(stream, msgType, payload); err != nil {
		_ = stream.Close()
		return nil, err
	}

	if msgType, payload, err = ReadMessage(stream); err != nil {
		_ = stream.Close()
		return nil, fmt.Errorf("error reading message: %v", err)
	}

	if msgType == MessageTypeError {
		_ = stream.Close()
		return nil, fmt.Errorf("error opening tunnel: %s", string(payload))
	}

	if msgType != MessageTypeOK {
		_ = stream.Close()
		return nil, fmt.Errorf("got unexpected init response: %d", msgType)
	}

	return stream, nil
}

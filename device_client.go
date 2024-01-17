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
	"log"
	"math/rand"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/xtaci/smux"
)

var deviceClientNetDialer = &net.Dialer{
	Timeout: 10 * time.Second,
}

// NewDeviceClient creates a new DeviceClient.
// endpoint is the address of the remote access device registration endpoint (e.g. "https://edge.example.com/device").
func NewDeviceClient(endpoint string, tlsConfig *tls.Config) (*DeviceClient, error) {
	// make sure that the edgeURL always has a port defined
	parsedURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to parse edge URL: %w", err)
	}

	if parsedURL.Scheme != "https" {
		return nil, fmt.Errorf("edge URL must use https scheme")
	}

	cli := &DeviceClient{
		endpoint:   endpoint,
		tlsConfig:  tlsConfig,
		smuxConfig: DefaultSmuxConfig,
	}

	return cli, nil
}

// DeviceClient is the remote access client for devices.
type DeviceClient struct {
	endpoint   string
	tlsConfig  *tls.Config
	smuxConfig *smux.Config
	runLock    sync.Mutex
	cancelCtx  context.CancelFunc
	err        error
	ready      sync.WaitGroup
}

// WithSmuxConfig sets the smux config for the device client.
func (cli *DeviceClient) WithSmuxConfig(smuxConfig *smux.Config) *DeviceClient {
	cli.smuxConfig = smuxConfig
	return cli
}

func (cli *DeviceClient) startOnce(ctx context.Context) error {
	smuxSession, err := ClientConnect(ctx, cli.endpoint, "", cli.tlsConfig)
	if err != nil {
		return err
	}
	defer smuxSession.Close()

	// mark client as ready and defer the removal of the ready marker
	cli.ready.Done()
	defer cli.ready.Add(1)

	// accept streams until the context is canceled due to an error or shutdown
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
			var stream *smux.Stream
			if stream, err = smuxSession.AcceptStream(); err != nil {
				return err
			}

			go handleStream(ctx, stream)
		}
	}
}

const (
	minReconnectDelay = 3
	maxReconnectDelay = 10
)

// Start starts the device client and processing loop.
// If connection to the edge service fails, the device client will retry to connect.
func (cli *DeviceClient) Start(ctx context.Context) {
	// prevent concurrent runs
	if !cli.runLock.TryLock() {
		return
	}

	cli.ready.Add(1)

	// use a context with cancel to cancelCtx the loop
	ctx, cli.cancelCtx = context.WithCancel(ctx)

	go func() {
		defer cli.runLock.Unlock()
		defer cli.cancelCtx()

		for {
			select {
			case <-ctx.Done():
				return
			default:
				if err := cli.startOnce(ctx); err != nil {
					reconnectIn := minReconnectDelay + rand.Int63n(maxReconnectDelay-minReconnectDelay)
					log.Printf("device client error: %v - reconnecting in %d seconds", err, reconnectIn)
					time.Sleep(time.Duration(reconnectIn) * time.Second)
				}
			}
		}
	}()
}

// IsRunning returns true if the device client is currently running.
func (cli *DeviceClient) IsRunning() bool {
	if cli.runLock.TryLock() {
		cli.runLock.Unlock()
		return false
	}

	return true
}

// Ready returns when the device client is ready.
func (cli *DeviceClient) Ready() {
	cli.ready.Wait()
}

// Err returns the error that caused the device client to stop.
func (cli *DeviceClient) Err() error {
	return cli.err
}

// Close kills all streams and disconnects from the edge service.
func (cli *DeviceClient) Close() error {
	if !cli.IsRunning() {
		return nil
	}

	if cli.cancelCtx != nil {
		cli.cancelCtx()
	}

	return nil
}

var deviceDialer = net.Dialer{}

// handleStream handles a new stream and log any errors.
func handleStream(ctx context.Context, stream *smux.Stream) {
	// ensure the stream is always closed
	defer stream.Close()

	// each stream starts with a message defining the type of tunnel
	messageType, payload, err := ReadMessage(stream)
	if err != nil {
		log.Printf("failed to read message: %v", err)
	}

	switch messageType {
	case MessageTypeTCPTunnel:
		err = HandleTCPTunnel(ctx, stream, string(payload))
	case MessageTypeUDPTunnel:
		err = HandleUDPTunnel(ctx, stream, string(payload))
	default:
		err = fmt.Errorf("unsupported message type: %v", messageType)
	}

	if err != nil {
		log.Printf("stream processing failed: %v", err)
	}
}

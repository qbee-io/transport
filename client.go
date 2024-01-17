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
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/xtaci/smux"
)

// Client is the client for the remote access service.
type Client struct {
	smuxSession *smux.Session
}

// NewClient creates a new remote access client instance connected to the given host.
func NewClient(ctx context.Context, endpointURL, authToken string, tlsConfig *tls.Config) (*Client, error) {
	smuxSession, err := ClientConnect(ctx, endpointURL, authToken, tlsConfig)
	if err != nil {
		return nil, err
	}

	cli := &Client{
		smuxSession: smuxSession,
	}

	return cli, nil
}

// Close closes the client.
func (cli *Client) Close() error {
	return cli.smuxSession.Close()
}

// OpenTCPTunnel listens on the given local port and forwards all connections to the given remote host and port.
// The returned listener can be used to close the tunnel.
func (cli *Client) OpenTCPTunnel(ctx context.Context, localHostPort, remoteHostPort string) (*net.TCPListener, error) {
	localAddr, err := net.ResolveTCPAddr("tcp", localHostPort)
	if err != nil {
		return nil, err
	}

	var tcpListener *net.TCPListener
	if tcpListener, err = net.ListenTCP("tcp", localAddr); err != nil {
		return nil, err
	}

	go func() {
		for {
			if err = ctx.Err(); err != nil {
				return
			}

			_ = tcpListener.SetDeadline(time.Now().Add(getIOWaitTimeout(ctx)))

			var tcpConnection *net.TCPConn
			if tcpConnection, err = tcpListener.AcceptTCP(); err != nil {
				// ignore deadline exceeded errors, as they are expected here,
				// so we can regularly check the context error
				if errors.Is(err, os.ErrDeadlineExceeded) {
					continue
				}

				if errors.Is(err, net.ErrClosed) {
					return
				}

				log.Printf("error accepting TCP localListener: %v", err)
				return
			}

			go func() {
				defer tcpConnection.Close()

				if connErr := NewTCPTunnel(ctx, tcpConnection, cli.smuxSession, remoteHostPort); connErr != nil {
					log.Printf("error forwarding TCP connection: %v", connErr)
				}
			}()
		}
	}()

	return tcpListener, err
}

// OpenUDPTunnel listens on the given local port and forwards all packets to the given remote host and port.
// Remember to close the tunnel when you're done with it using the Close method.
func (cli *Client) OpenUDPTunnel(ctx context.Context, localHostPort, remoteHostPort string) (*UDPTunnel, error) {
	_, port, err := net.SplitHostPort(remoteHostPort)
	if err != nil {
		return nil, fmt.Errorf("error parsing remote host port: %v", err)
	}

	var dstPort uint64
	if dstPort, err = strconv.ParseUint(port, 10, 16); err != nil {
		return nil, fmt.Errorf("error parsing remote port: %v", err)
	}

	var udpLocalAddr *net.UDPAddr
	if udpLocalAddr, err = net.ResolveUDPAddr("udp", localHostPort); err != nil {
		return nil, fmt.Errorf("error resolving local UDP address: %v", err)
	}

	var localListener *net.UDPConn
	if localListener, err = net.ListenUDP("udp", udpLocalAddr); err != nil {
		return nil, fmt.Errorf("error creating local listener for %s: %v", udpLocalAddr, err)
	}

	udpTunnel := &UDPTunnel{
		ctx:            ctx,
		ioWaitTimeout:  getIOWaitTimeout(ctx),
		listenerAddr:   *udpLocalAddr,
		dstPort:        uint16(dstPort),
		remoteHostPort: remoteHostPort,
		session:        cli.smuxSession,
		localListener:  localListener,
		streams:        make(map[string]*UDPStream),
	}

	go udpTunnel.start()

	return udpTunnel, nil
}

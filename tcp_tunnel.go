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

	"github.com/xtaci/smux"
)

// NewTCPConn tunnels local TCP connection through smux stream to the given remote host and port.
func (cli *Client) newTCPConnection(ctx context.Context, conn *net.TCPConn, remoteHostPort string) error {
	stream, err := cli.OpenStream(ctx, MessageTypeTCPTunnel, []byte(remoteHostPort))
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()

	_, _, err = Pipe(conn, stream)

	return err
}

// HandleTCPTunnel handles a TCP tunnel request as a device.
func HandleTCPTunnel(ctx context.Context, stream *smux.Stream, remoteAddr []byte) error {
	tcpConn, err := deviceDialer.DialContext(ctx, "tcp", string(remoteAddr))
	if err != nil {
		return WriteError(stream, err)
	}
	defer func() { _ = tcpConn.Close() }()

	if err = WriteOK(stream, nil); err != nil {
		return err
	}

	_, _, err = Pipe(tcpConn, stream)

	return err
}

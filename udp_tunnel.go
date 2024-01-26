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
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xtaci/smux"
)

// UDPStream represents a stream of UDP datagrams between a local client and a remote server.
type UDPStream struct {
	// ctx is the context for the stream.
	// It's used to cancel the stream.
	ctx context.Context

	// lock controls concurrent access to the listeners map or expires timer.
	lock sync.Mutex

	// ioWaitTimeout is the timeout for all I/O operations.
	ioWaitTimeout time.Duration

	// expires is timer which will close the stream after the defined inactivity timeout.
	// This timer is rescheduled whenever we send or receive data through the stream.
	expires *time.Timer

	// cliAddr is the address of the client who initiated the stream.
	cliAddr *net.UDPAddr

	// remoteDstPort is the requested remote destination port for the UDP tunnel.
	remoteDstPort uint16

	// listenerAddr is the address of the primary local listener which we pass down to secondary listeners.
	listenerAddr net.UDPAddr

	// listeners is a map of remote source port to listeners.
	listeners map[uint16]*net.UDPConn

	// stream is the smux stream for the tunnel.
	stream *smux.Stream

	// writeLock controls concurrent write access to the stream
	writeLock sync.Mutex
}

// write data for the given destination port to the stream.
// headerBuf is a buffer to reuse for writing the destination port and the data length.
// data is the actual data to be written.
//
// If any write to the stream fails, we close the entire stream,
// as we cannot guarantee data integrity anymore.
func (s *UDPStream) write(dstPort uint16, headerBuf, data []byte) error {
	binary.BigEndian.PutUint16(headerBuf, dstPort)
	binary.BigEndian.PutUint16(headerBuf[2:], uint16(len(data)))

	s.writeLock.Lock()
	defer s.writeLock.Unlock()

	_ = s.stream.SetWriteDeadline(time.Now().Add(s.ioWaitTimeout))
	if _, err := s.stream.Write(headerBuf); err != nil {
		log.Printf("[client:stream] error writing UDP header for %d: %v", dstPort, err)
		// at this point, we cannot guarantee whether the data was written or not,
		// so we close the stream to avoid data corruption.
		s.close()
		return err
	}

	if _, err := s.stream.Write(data); err != nil {
		log.Printf("[client:stream] error writing UDP header for %d: %v", dstPort, err)
		// at this point, we cannot guarantee whether the data was written or not,
		// so we close the stream to avoid data corruption.
		s.close()
		return err
	}

	s.lock.Lock()
	s.expires.Reset(clientStreamTTL)
	s.lock.Unlock()

	return nil
}

// clientStreamTTL is the timeout for client streams after which they are automatically closed.
const clientStreamTTL = 15 * time.Minute

// processListener reads UDP packets from the given listener and forwards them to the stream.
func (s *UDPStream) processSecondaryListener(dstPort uint16, listener *net.UDPConn) {
	var err error
	var dataLength int
	var srcAddr *net.UDPAddr

	headerBuf := make([]byte, 4)
	dataBuf := make([]byte, maxUDPPacketSize)

	for {
		_ = listener.SetReadDeadline(time.Now().Add(clientStreamTTL))

		if dataLength, srcAddr, err = listener.ReadFromUDP(dataBuf); err != nil {
			// we don't want to keep secondary listeners open forever,
			// so we close them gracefully after a certain idle timeout.
			if errors.Is(err, os.ErrDeadlineExceeded) {
				_ = listener.Close()

				s.lock.Lock()
				delete(s.listeners, dstPort)
				s.lock.Unlock()

				return
			}

			if errors.Is(err, net.ErrClosed) {
				s.lock.Lock()
				delete(s.listeners, dstPort)
				s.lock.Unlock()

				return
			}

			log.Printf("[client:secondary-listener] error reading UDP data for %d: %v", dstPort, err)
			return
		}

		// discard packets from other clients
		if !srcAddr.IP.Equal(s.cliAddr.IP) {
			log.Printf("[client:secondary-listener] got a foreign packet at %d: %v", dstPort, err)
			continue
		}

		if err = s.write(dstPort, headerBuf, dataBuf[:dataLength]); err != nil {
			log.Printf("[client:secondary-listener] error writing UDP data for %d: %v", dstPort, err)
			return
		}
	}
}

// getOrCreateListener returns a listener for the given source port.
// If a listener for the given source port already exists, it returns it.
// Otherwise, it creates a new listener, starts processing it and returns it.
// When we create a new listener, we attempt to use the remote source port as the local port.
// If that fails, we use a random port.
func (s *UDPStream) getOrCreateListener(remoteSrcPort uint16) (*net.UDPConn, error) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if listener, ok := s.listeners[remoteSrcPort]; ok {
		return listener, nil
	}

	listenerAddr := s.listenerAddr         // copy the listener address from the main listener
	listenerAddr.Port = int(remoteSrcPort) // and try to use the remote source port

	listener, err := net.ListenUDP("udp", &listenerAddr)

	// if we failed to use the remote source port, let's use a random port
	if errno := syscall.Errno(0); errors.As(err, &errno) && errno == syscall.EADDRINUSE {
		listenerAddr.Port = 0
		listener, err = net.ListenUDP("udp", &listenerAddr)
	}

	if err != nil {
		return nil, fmt.Errorf("error creating secondary listener for %s: %v", listenerAddr.String(), err)
	}

	go s.processSecondaryListener(remoteSrcPort, listener)

	s.listeners[remoteSrcPort] = listener

	return listener, nil
}

// start reads UDP packets from the stream and forwards them to the appropriate listener.
// When a packet is received from a new remote source port, a new listener is created.
func (s *UDPStream) start() {
	// setup panic recovery for the stream, so we don't affect other streams or tunnels
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[client:udp-stream] panic: %v", r)
		}
	}()

	var err error
	var dataLength int
	var remoteSrcPort uint16
	var currentListener *net.UDPConn

	headerBuf := make([]byte, 4)
	dataBuf := make([]byte, maxUDPPacketSize)

	for {
		if s.ctx.Err() != nil {
			return
		}

		_ = s.stream.SetReadDeadline(time.Now().Add(s.ioWaitTimeout))
		if _, err = io.ReadFull(s.stream, headerBuf); err != nil {
			// ignore deadline exceeded errors, as they are expected here,
			// so we can regularly check the context error.
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}

			return
		}

		s.lock.Lock()
		s.expires.Reset(clientStreamTTL)
		s.lock.Unlock()

		remoteSrcPort = binary.BigEndian.Uint16(headerBuf)
		dataLength = int(binary.BigEndian.Uint16(headerBuf[2:]))

		if _, err = io.ReadFull(s.stream, dataBuf[:dataLength]); err != nil {
			return
		}

		if currentListener, err = s.getOrCreateListener(remoteSrcPort); err != nil {
			log.Printf("[client:stream] cannot get listener for %d: %v", remoteSrcPort, err)
			return
		}

		if _, err = currentListener.WriteToUDP(dataBuf[:dataLength], s.cliAddr); err != nil {
			return
		}
	}
}

// close the stream and all its listeners.
func (s *UDPStream) close() {
	s.lock.Lock()
	defer s.lock.Unlock()

	_ = s.stream.Close()

	for remoteSrcPort, listener := range s.listeners {
		// don't close the primary listener
		if remoteSrcPort == s.remoteDstPort {
			continue
		}

		_ = listener.Close()
	}
}

// UDPTunnel is a client implementation of a tunnel for UDP datagrams exchange.
// It listens on a local port and for each new client it receives a packet from,
// it creates a new smux stream to isolate and simplify the communication.
// When remote responds from a different port, it creates a new listener for that remote source port locally,
// so we can send the response to the client from a separate port as well.
type UDPTunnel struct {
	// ctx is the context for the tunnel.
	// It's used to cancel the tunnel.
	ctx context.Context

	// ioWaitTimeout is the timeout for all I/O operations.
	ioWaitTimeout time.Duration

	// remoteHostPort is the remote host and port for the UDP tunnel.
	remoteHostPort string

	// dstPort defines the remote destination port for the UDP tunnel.
	dstPort uint16

	// listenerAddr is the address of the local listener which we pass down to UDPStreams,
	// so secondary listeners for the client listen on the same IP address.
	listenerAddr net.UDPAddr

	// localListener is the main local listener for the tunnel.
	localListener *net.UDPConn

	// session is the smux session for the tunnel.
	// It's used to create new streams for new clients.
	session *smux.Session

	// streams is a map of client address (host:port) to streams.
	// We use a separate stream for each client to provide isolation.
	streams map[string]*UDPStream
}

const maxUDPPacketSize = 65535

// PrimaryAddr returns the primary address of the local listener.
func (t *UDPTunnel) PrimaryAddr() *net.UDPAddr {
	return t.localListener.LocalAddr().(*net.UDPAddr)
}

// start reads UDP packets from the local listeners and forwards them through the tunnel.
// When a packet is received from a new client (host:port), a new stream is created.
// When a packet is received from an existing client, it is forwarded through its existing stream.
func (t *UDPTunnel) start() {
	// setup panic recovery for the tunnel, so we don't affect other active tunnels
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[client:udp-tunnel] panic: %v", r)
		}
	}()

	var err error
	var dataLength int
	var cliAddr *net.UDPAddr
	var stream *UDPStream

	headerBuf := make([]byte, 4)
	dataBuf := make([]byte, maxUDPPacketSize)

	for {
		if t.ctx.Err() != nil {
			return
		}

		// we don't need deadline here, as the client will close the local listener when it's done
		if dataLength, cliAddr, err = t.localListener.ReadFromUDP(dataBuf); err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}

			log.Printf("[client:listener] error reading UDP data: %v", err)
			return
		}

		if stream, err = t.getOrCreateStream(cliAddr, t.localListener); err != nil {
			log.Printf("error creating UDP stream for %s: %v", cliAddr, err)
			continue
		}

		if err = stream.write(t.dstPort, headerBuf, dataBuf[:dataLength]); err != nil {
			log.Printf("error writing UDP data to %s: %v", cliAddr, err)
			continue
		}
	}
}

// getOrCreateStream creates a new UDP stream between client and remote.
func (t *UDPTunnel) getOrCreateStream(cliAddr *net.UDPAddr, listener *net.UDPConn) (*UDPStream, error) {
	if stream, ok := t.streams[cliAddr.String()]; ok {
		return stream, nil
	}

	// We want to send the client/source port suggestion to the server, so we can use it on the device.
	// The device will attempt to use it, but if it fails, it will use a random port
	payload := fmt.Sprintf("%d:%s", cliAddr.Port, t.remoteHostPort)

	stream, err := OpenStream(t.ctx, t.session, MessageTypeUDPTunnel, []byte(payload))
	if err != nil {
		return nil, fmt.Errorf("error opening stream: %v", err)
	}

	udpStream := &UDPStream{
		ctx:           t.ctx,
		ioWaitTimeout: t.ioWaitTimeout,
		cliAddr:       cliAddr,
		remoteDstPort: t.dstPort,
		listenerAddr:  t.listenerAddr,
		expires: time.AfterFunc(clientStreamTTL, func() {
			if streamToClose, ok := t.streams[cliAddr.String()]; ok {
				streamToClose.close()
				delete(t.streams, cliAddr.String())
			}
		}),
		listeners: map[uint16]*net.UDPConn{
			t.dstPort: listener,
		},
		stream: stream,
	}

	t.streams[cliAddr.String()] = udpStream

	go udpStream.start()

	return udpStream, nil
}

// Close closes the tunnel.
func (t *UDPTunnel) Close() error {
	err := t.localListener.Close()

	for _, stream := range t.streams {
		stream.close()
	}

	return err
}

// newDeviceUDPListener creates a new UDP listener for the given destination address.
// Provided suggestedPort will be used if available, otherwise a random port will be used.
func newDeviceUDPListener(dstAddr *net.UDPAddr, suggestedPort string) (*net.UDPConn, error) {
	// empty listener host will listen on all available interfaces
	listenerHost := ""

	// if destination is localhost, let's listen on localhost to limit exposure
	if dstAddr.IP.IsLoopback() {
		// for IPv4 destinations, let's listen on IPv4 localhost
		if ip4 := dstAddr.IP.To4(); ip4 != nil {
			listenerHost = "127.0.0.1"
		} else { // otherwise, let's listen on IPv6 localhost
			listenerHost = "[::1]"
		}
	}

	srcAddresses := []string{
		fmt.Sprintf("%s:%s", listenerHost, suggestedPort), // first, try the suggested port
		listenerHost + ":0", // if that fails, use a random port allocated by the OS
	}

	var err error
	var srcAddr *net.UDPAddr
	var udpListener *net.UDPConn

	for _, addr := range srcAddresses {
		if srcAddr, err = net.ResolveUDPAddr("udp", addr); err != nil {
			continue
		}

		if udpListener, err = net.ListenUDP("udp", srcAddr); err != nil {
			continue
		}

		break
	}
	if err != nil {
		return nil, err
	}

	return udpListener, nil
}

// handleUDPTunnelTx forwards UDP packets from the local listener to the client stream inside the tunnel.
func handleUDPTunnelTx(ctx context.Context, localListener *net.UDPConn, stream *smux.Stream) {
	var err error
	var dataLength int
	var srcAddr *net.UDPAddr

	headerBuf := make([]byte, 4)
	dataBuf := make([]byte, maxUDPPacketSize)

	ioWaitTimeoutDuration := getIOWaitTimeout(ctx)

	for {
		if ctx.Err() != nil {
			return
		}

		_ = localListener.SetReadDeadline(time.Now().Add(ioWaitTimeoutDuration))
		if dataLength, srcAddr, err = localListener.ReadFromUDP(dataBuf); err != nil {
			// ignore deadline exceeded errors, as they are expected here,
			// so we can regularly check the context error
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}

			if errors.Is(err, net.ErrClosed) {
				return
			}

			log.Printf("[device:listener] error reading UDP data: %v", err)
			return
		}

		binary.BigEndian.PutUint16(headerBuf, uint16(srcAddr.Port))
		binary.BigEndian.PutUint16(headerBuf[2:], uint16(dataLength))

		_ = stream.SetWriteDeadline(time.Now().Add(ioWaitTimeoutDuration))
		if _, err = stream.Write(headerBuf); err != nil {
			log.Printf("[device:stream] error writing UDP header: %v", err)
			return
		}

		if _, err = stream.Write(dataBuf[:dataLength]); err != nil {
			log.Printf("[device:stream] error writing UDP data: %v", err)
			return
		}
	}
}

// handleUDPTunnelRx forwards UDP packets from the client stream inside the tunnel to the local listener.
func handleUDPTunnelRx(ctx context.Context, stream *smux.Stream, udpListener *net.UDPConn, dstAddr *net.UDPAddr) error {
	var err error
	var dataLength int

	headerBuf := make([]byte, 4)
	dataBuf := make([]byte, maxUDPPacketSize)

	ioWaitTimeoutDuration := getIOWaitTimeout(ctx)

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		_ = stream.SetReadDeadline(time.Now().Add(ioWaitTimeoutDuration))
		if _, err = io.ReadFull(stream, headerBuf); err != nil {
			// ignore deadline exceeded errors, as they are expected here,
			// so we can regularly check the context error
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			}

			return err
		}

		dstAddr.Port = int(binary.BigEndian.Uint16(headerBuf))
		dataLength = int(binary.BigEndian.Uint16(headerBuf[2:]))

		if _, err = io.ReadFull(stream, dataBuf[:dataLength]); err != nil {
			return err
		}

		_ = udpListener.SetWriteDeadline(time.Now().Add(ioWaitTimeoutDuration))
		if _, err = udpListener.WriteToUDP(dataBuf[:dataLength], dstAddr); err != nil {
			return err
		}
	}
}

// HandleUDPTunnel handles a UDP tunnel stream as a device.
func HandleUDPTunnel(ctx context.Context, stream *smux.Stream, payload []byte) error {
	payloadParts := strings.SplitN(string(payload), ":", 2)
	if len(payloadParts) != 2 {
		return WriteError(stream, fmt.Errorf("invalid payload"))
	}

	suggestedSrcPort, remoteAddr := payloadParts[0], payloadParts[1]

	dstAddr, err := net.ResolveUDPAddr("udp", remoteAddr)
	if err != nil {
		return WriteError(stream, err)
	}

	var udpListener *net.UDPConn
	if udpListener, err = newDeviceUDPListener(dstAddr, suggestedSrcPort); err != nil {
		return WriteError(stream, err)
	}
	defer udpListener.Close()

	if err = WriteOK(stream, nil); err != nil {
		return err
	}

	go handleUDPTunnelTx(ctx, udpListener, stream)

	return handleUDPTunnelRx(ctx, stream, udpListener, dstAddr)
}

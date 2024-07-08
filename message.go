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
	"encoding/binary"
	"fmt"
	"io"
)

// MessageType defines the type of message sent over the wire.
type MessageType uint8

const (
	// MessageTypeOK indicates that requested operation was successful.
	// The payload is empty.
	MessageTypeOK MessageType = 0x01

	// MessageTypeError indicates that requested operation failed.
	// The payload contains the error message.
	MessageTypeError MessageType = 0x02

	// MessageTypeGoAway indicates that client should reconnect to another server.
	MessageTypeGoAway MessageType = 0x03

	// MessageTypeTCPTunnel indicates that the message is a TCP tunnel request.
	// The payload contains the remote host and port.
	MessageTypeTCPTunnel MessageType = 0x10

	// MessageTypeUDPTunnel indicates that the message is a UDP tunnel request.
	// The payload contains the suggested listener port, remote host and port.
	MessageTypeUDPTunnel MessageType = 0x11

	// MessageTypePTY indicates that the message is a PTY request.
	// The payload contains initial JSON-encoded PTYCommand with PTYCommandTypeResize to set the initial window size.
	MessageTypePTY MessageType = 0x12

	// MessageTypePTYCommand indicates that the message is a PTY command request.
	// The payload contains JSON-encoded PTYCommand.
	MessageTypePTYCommand MessageType = 0x13

	// MessageTypeReload triggers a configuration reload.
	MessageTypeReload MessageType = 0x14

	// MessageTypeCommand is a command to be executed without a PTY.
	MessageTypeCommand MessageType = 0x15
)

// Message wire format:
// 1 byte: message type
// 2 bytes: message length (n)
// n bytes: message payload

const maxPayloadLength = 65535

// WriteMessage writes a message to the given writer.
func WriteMessage(w io.Writer, messageType MessageType, payload []byte) error {
	if len(payload) > maxPayloadLength {
		return fmt.Errorf("payload too large: %d bytes", len(payload))
	}

	header := make([]byte, 3)
	header[0] = byte(messageType)
	binary.BigEndian.PutUint16(header[1:], uint16(len(payload)))

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("error writing message header: %v", err)
	}

	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("error writing message payload: %v", err)
	}

	return nil
}

// ReadMessage reads a message from the given reader.
func ReadMessage(r io.Reader) (messageType MessageType, payload []byte, err error) {
	header := make([]byte, 3)
	if _, err = io.ReadFull(r, header); err != nil {
		return 0, nil, fmt.Errorf("error reading message header: %v", err)
	}

	messageType = MessageType(header[0])
	payloadLength := binary.BigEndian.Uint16(header[1:])

	payload = make([]byte, payloadLength)
	if _, err = io.ReadFull(r, payload); err != nil {
		return 0, nil, fmt.Errorf("error reading message payload: %v", err)
	}

	return messageType, payload, nil
}

// WriteError writes an error message to the given writer.
func WriteError(w io.Writer, err error) error {
	_ = WriteMessage(w, MessageTypeError, []byte(err.Error()))
	return err
}

// WriteOK writes an OK message to the given writer.
func WriteOK(w io.Writer, payload []byte) error {
	return WriteMessage(w, MessageTypeOK, payload)
}

// ExpectOK reads a message from the given reader and returns the payload if it is an OK message.
// Otherwise, an error is returned.
func ExpectOK(r io.Reader) ([]byte, error) {
	msgType, payload, err := ReadMessage(r)
	if err != nil {
		return nil, fmt.Errorf("error reading message: %v", err)
	}

	if msgType == MessageTypeError {
		return nil, fmt.Errorf("remote error: %s", payload)
	}

	if msgType != MessageTypeOK {
		return nil, fmt.Errorf("got unexpected init response: %d", msgType)
	}

	return payload, nil
}

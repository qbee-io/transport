// Copyright 2023 qbee.io
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

package transport

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// failingReadWriter fails all the read and write operations with the provided error.
type failingReadWriter struct {
	err error
}

func (f failingReadWriter) Read([]byte) (int, error)  { return 0, f.err }
func (f failingReadWriter) Write([]byte) (int, error) { return 0, f.err }

// Test_MessageErrorsWrapCause makes sure callers can inspect the cause of the message read/write errors.
func Test_MessageErrorsWrapCause(t *testing.T) {
	rw := failingReadWriter{err: io.ErrClosedPipe}

	t.Run("ReadMessage", func(t *testing.T) {
		if _, _, err := ReadMessage(rw); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("expected error wrapping io.ErrClosedPipe, got %v", err)
		}
	})

	t.Run("ReadMessage truncated payload", func(t *testing.T) {
		// header announcing 4 bytes of payload, followed by only 1 byte
		r := bytes.NewReader([]byte{byte(MessageTypeOK), 0x00, 0x04, 0x00})

		if _, _, err := ReadMessage(r); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("expected error wrapping io.ErrUnexpectedEOF, got %v", err)
		}
	})

	t.Run("ExpectOK", func(t *testing.T) {
		if _, err := ExpectOK(rw); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("expected error wrapping io.ErrClosedPipe, got %v", err)
		}
	})

	t.Run("ExpectOK on closed stream", func(t *testing.T) {
		if _, err := ExpectOK(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
			t.Fatalf("expected error wrapping io.EOF, got %v", err)
		}
	})

	t.Run("WriteMessage", func(t *testing.T) {
		if err := WriteMessage(rw, MessageTypeOK, nil); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("expected error wrapping io.ErrClosedPipe, got %v", err)
		}
	})
}

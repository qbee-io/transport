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
	"errors"
	"io"
)

// Pipe copies data from src to dst and vice versa and returns first non-nil error.
func Pipe(src io.ReadWriteCloser, dst io.ReadWriteCloser) (sent int64, received int64, err error) {
	errCh := make(chan error)

	go func() {
		var rxErr error
		received, rxErr = io.Copy(src, dst)
		errCh <- rxErr
	}()

	go func() {
		var txErr error
		sent, txErr = io.Copy(dst, src)
		errCh <- txErr
	}()

	err = <-errCh
	_ = src.Close()
	_ = dst.Close()

	if err == nil {
		err = <-errCh
	} else {
		<-errCh
	}

	// Ignore closed pipe and EOF errors.
	if errors.Is(err, io.ErrClosedPipe) || errors.Is(err, io.EOF) {
		err = nil
	}

	return
}

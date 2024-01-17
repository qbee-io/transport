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
	"fmt"
	"time"

	"github.com/xtaci/smux"
)

// initTunnelStream initializes a tunnel stream.
func initTunnelStream(
	ctx context.Context,
	session *smux.Session,
	msgType MessageType,
	remoteHostPort string,
) (*smux.Stream, error) {
	ioWaitTimeoutDuration := getIOWaitTimeout(ctx)

	_ = session.SetDeadline(time.Now().Add(ioWaitTimeoutDuration))
	stream, err := session.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("error opening stream: %v", err)
	}

	payload := []byte(remoteHostPort)
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

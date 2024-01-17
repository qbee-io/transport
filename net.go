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
	"fmt"
	"net"
	"strconv"
)

// ParsePortUint16 parses a string containing a port number and returns the port as uint16.
func ParsePortUint16(hostPort string) (uint16, error) {
	_, portString, err := net.SplitHostPort(hostPort)
	if err != nil {
		return 0, fmt.Errorf("error parsing host port: %v", err)
	}

	var port uint64
	if port, err = strconv.ParseUint(portString, 10, 16); err != nil {
		return 0, fmt.Errorf("error parsing port number: %v", err)
	}

	return uint16(port), nil
}

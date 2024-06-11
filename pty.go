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

// PTYCommandType is the type of PTY command.
type PTYCommandType uint8

const (
	// PTYCommandTypeResize indicates that the command is a resize request.
	PTYCommandTypeResize PTYCommandType = iota
)

// PTYCommand carries a command to be executed on the PTY stream.
type PTYCommand struct {
	// SessionID is the session ID (initiated with MessageTypePTY) to which the command applies.
	SessionID string `json:"sid"`

	// Type is the type of the command to be executed on the PTY.
	Type PTYCommandType `json:"type,omitempty"`

	// Cols and Rows are the new window size.
	// Those fields are only used when Type is PTYCommandTypeResize.
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`

	// Command is the command to be executed on the PTY.
	Command string `json:"command,omitempty"`

	// NoPTY is set to true if the command should not be executed on a PTY.
	NoPTY bool `json:"no_pty,omitempty"`
}

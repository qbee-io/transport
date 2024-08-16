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

type Command struct {
	// SessionID is the session ID to which the command applies.
	SessionID string `json:"sid"`

	// Command is the command to be executed on the PTY.
	Command string `json:"command,omitempty"`

	// CommandArgs are the arguments to be passed to the command.
	CommandArgs []string `json:"command_args,omitempty"`
}

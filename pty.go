package transport

type PTYCommandType uint8

const (
	// PTYCommandTypeResize indicates that the command is a resize request.
	PTYCommandTypeResize PTYCommandType = iota
)

// PTYCommand carries a command to be executed on the PTY stream.
type PTYCommand struct {
	// Type is the type of the command to be executed on the PTY.
	Type PTYCommandType `json:"type,omitempty"`

	// StreamID is the ID of the stream (initiated with MessageTypePTY) to which the command applies.
	StreamID uint32 `json:"sid"`

	// Cols and Rows are the new window size.
	// Those fields are only used when Type is PTYCommandTypeResize.
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
}

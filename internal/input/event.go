package input

const (
	EVSyn = 0x00
	EVKey = 0x01
	EVMsc = 0x04
	EVLed = 0x11

	SynReport  = 0
	SynDropped = 3

	MscScan = 4

	LEDNumLock    = 0
	LEDCapsLock   = 1
	LEDScrollLock = 2
)

// Event is the architecture-independent portion of Linux's input_event.
// Kernel timestamps are deliberately omitted: the matcher uses the monotonic
// Go clock for deadlines, and uinput ignores injected event timestamps.
type Event struct {
	Type  uint16
	Code  uint16
	Value int32
}

// Frame contains every event up to and including one SYN_REPORT. Keeping
// frames intact prevents scan codes and key state changes from being split.
type Frame struct {
	Device string
	Events []Event
}

func (f Frame) Clone() Frame {
	copyOfEvents := append([]Event(nil), f.Events...)
	return Frame{Device: f.Device, Events: copyOfEvents}
}

// Message is emitted by the Linux device manager. Exactly one of Frame,
// Resync, Removed, or Err is meaningful.
type Message struct {
	Frame   *Frame
	Resync  *Resync
	Removed string
	Err     error
}

// Resync is generated after SYN_DROPPED. Pressed is the authoritative state
// returned by EVIOCGKEY after the next SYN_REPORT.
type Resync struct {
	Device  string
	Pressed map[uint16]bool
}

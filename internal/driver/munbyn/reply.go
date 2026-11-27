package munbyn

import (
	"fmt"
	"strconv"
	"strings"
)

// Event is field 1 of every command and reply.
type Event uint32

// Events crate-labels sends or reads.
const (
	EventDeviceInfo   Event = 1
	EventDevicePrint  Event = 4
	EventDeviceReport Event = 6
	EventPrintEnd     Event = 10
	EventPaperTypeSet Event = 11
)

// PaperType is the argument of PAPER_TYPE_SET and field 10 of the DEVICE_INFO answer.
type PaperType int32

// Paper types. crate-labels never asks for BlackMark.
const (
	PaperGap        PaperType = 1
	PaperContinuous PaperType = 2
	PaperBlackMark  PaperType = 3
)

// String names the paper type for messages.
func (p PaperType) String() string {
	if name, ok := map[PaperType]string{PaperGap: "gap", PaperContinuous: "continuous", PaperBlackMark: "black mark"}[p]; ok {
		return name
	}
	return "paper type " + strconv.Itoa(int(p))
}

// codeOK is the reply code for success (field 2).
const codeOK = 200

// Envelope builds a command frame: event in field 1, a signed argument in field 4, and
// a body in field 5.
func Envelope(event Event, arg int32, body []byte) ([]byte, error) {
	var b Builder
	return Wrap(b.Uint(1, uint32(event)).Signed(4, arg).Blob(5, body).Payload())
}

func fixedCommand(event Event, arg int32) []byte {
	frame, err := Envelope(event, arg, nil)
	if err != nil {
		panic(err) // a fixed command is a few bytes
	}
	return frame
}

// Command frames without a body.
var (
	DeviceInfoCommand = fixedCommand(EventDeviceInfo, 0)
	PrintEndCommand   = fixedCommand(EventPrintEnd, 0)
)

// PaperTypeSetCommand is the PAPER_TYPE_SET frame for p.
func PaperTypeSetCommand(p PaperType) []byte { return fixedCommand(EventPaperTypeSet, int32(p)) }

// Kind says what a reply means for a print job.
type Kind uint8

// Reply kinds.
const (
	KindUnknown    Kind = iota // anything a job does not act on
	KindInfo                   // DEVICE_INFO answered with 200
	KindPaperSet               // PAPER_TYPE_SET answered with 200
	KindRejected               // DEVICE_PRINT answered with another code
	KindSectionAck             // report 11 with state "2": a section was taken
	KindLabelDone              // report 10: the label finished, State holds problem bits
)

var kindNames = [...]string{"unknown", "info", "paper-set", "rejected", "section-ack", "label-done"}

// String names the kind, for tests and logs.
func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "kind " + strconv.Itoa(int(k))
}

// Reply is one decoded notification payload.
type Reply struct {
	Kind  Kind
	Event Event
	Code  int32 // field 2 (0 when absent)

	// DEVICE_REPORT: the report number and its decimal state string, both from field 3.
	Report int32
	State  int32

	// DEVICE_INFO, from field 3.
	Paper    PaperType
	Firmware string
	Battery  int32 // units unknown
}

// Report numbers inside DEVICE_REPORT.
const (
	reportLabelDone = 10
	reportSection   = 11
	sectionTaken    = 2 // the state of report 11 that acknowledges a section
)

// readers fill in a reply from its nested message (field 3), keyed by event.
var readers = map[Event]func(r *Reply, nested map[int]Field){
	EventDeviceInfo: func(r *Reply, n map[int]Field) {
		if r.Code == codeOK {
			r.Kind, r.Paper, r.Firmware, r.Battery = KindInfo, PaperType(n[10].Int32()), n[3].Text(), n[5].Int32()
		}
	},
	EventPaperTypeSet: func(r *Reply, _ map[int]Field) {
		if r.Code == codeOK {
			r.Kind = KindPaperSet
		}
	},
	EventDevicePrint: func(r *Reply, _ map[int]Field) {
		if r.Code != codeOK {
			r.Kind = KindRejected
		}
	},
	EventDeviceReport: func(r *Reply, n map[int]Field) {
		r.Report = n[1].Int32()
		// The state is a decimal string; anything else reads as 0.
		if v, err := strconv.ParseInt(n[2].Text(), 10, 32); err == nil {
			r.State = int32(v)
		}
		switch {
		case r.Report == reportLabelDone:
			r.Kind = KindLabelDone
		case r.Report == reportSection && r.State == sectionTaken:
			r.Kind = KindSectionAck
		}
	},
}

// Decode reads a reassembled payload.
func Decode(payload []byte) Reply {
	top := DecodeFields(payload)
	r := Reply{Event: Event(uint32(top[1].Int32())), Code: top[2].Int32()}
	nested := map[int]Field{}
	if f := top[3]; f.IsBytes {
		nested = DecodeFields(f.Bytes)
	}
	if read, ok := readers[r.Event]; ok {
		read(&r, nested)
	}
	return r
}

// Condition is one problem bit of a finished label's state.
type Condition struct {
	Bit     uint
	Name    string
	Message string
}

// conditions are the problem bits the FM226 reports, most urgent first. When several
// bits are set, the first one listed here is the one the user hears about. Physical
// blockers come first (the lid, a jam, the roll), then heat, then power, then states
// that clear by themselves.
var conditions = []Condition{
	{3, "lid-open", "The printer's lid is open. Shut it, then print again."},
	{4, "jammed", "Labels are stuck inside the printer. Clear them out, then print again."},
	{1, "no-labels", "There are no labels left. Put a new roll in the printer, then print again."},
	{5, "head-hot", "The print head has overheated. Let it cool for a minute, then print again."},
	{7, "motor-hot", "The printer's motor has overheated. Let it cool for a minute, then print again."},
	{6, "battery-low", "The printer's battery is running low. Plug it in, then print again."},
	{8, "measuring", "The printer is still measuring its labels. Wait until it stops, then print again."},
	{2, "memory-full", "The printer's memory is full. Give it a few seconds, then print again."},
	{0, "busy", "The printer is busy with something else. Give it a few seconds, then print again."},
}

// Conditions lists the known problem bits set in state, most urgent first.
func Conditions(state int32) []Condition {
	var out []Condition
	for _, c := range conditions {
		if uint32(state)>>c.Bit&1 != 0 {
			out = append(out, c)
		}
	}
	return out
}

// Problem is the user message for a finished label's state, or "" for 0.
func Problem(state int32) string {
	if state == 0 {
		return ""
	}
	if cs := Conditions(state); len(cs) > 0 {
		return cs[0].Message
	}
	return fmt.Sprintf("The printer sent status code %d, which crate-labels does not recognise.", state)
}

// conditionNames joins the names of every known bit in state, for logs and details.
func conditionNames(state int32) string {
	var names []string
	for _, c := range Conditions(state) {
		names = append(names, c.Name)
	}
	return strings.Join(names, ",")
}

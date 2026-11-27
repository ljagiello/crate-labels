// Package ipp is a small IPP/1.1 client for page and label printers on the local
// network.
//
// It covers the message encoding of RFC 8010, the operations and attributes of RFC 8011
// that a print client needs, media collections (PWG 5100.7), the IPP Everywhere printer
// description (PWG 5100.14), an Apple raster (URF) encoder whose line coding is the one
// PWG 5102.4 defines for PWG Raster, and DNS-SD discovery of _ipp._tcp services.
package ipp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Tag is an IPP delimiter tag (0x00-0x0f, which opens an attribute group) or a value
// tag (0x10-0xff), RFC 8010 section 3.5.
type Tag byte

// Delimiter tags, RFC 8010 section 3.5.1.
const (
	TagOperationGroup   Tag = 0x01
	TagJobGroup         Tag = 0x02
	TagEnd              Tag = 0x03
	TagPrinterGroup     Tag = 0x04
	TagUnsupportedGroup Tag = 0x05
)

// Value tags, RFC 8010 section 3.5.2. The out-of-band ones listed here are those of
// RFC 8010 Table 3; IsOutOfBand covers the whole registered range.
const (
	TagUnsupportedValue Tag = 0x10
	TagUnknown          Tag = 0x12
	TagNoValue          Tag = 0x13
	TagInteger          Tag = 0x21
	TagBoolean          Tag = 0x22
	TagEnum             Tag = 0x23
	TagOctetString      Tag = 0x30
	TagDateTime         Tag = 0x31
	TagResolution       Tag = 0x32
	TagRangeOfInteger   Tag = 0x33
	TagBeginCollection  Tag = 0x34
	TagTextWithLanguage Tag = 0x35
	TagNameWithLanguage Tag = 0x36
	TagEndCollection    Tag = 0x37
	TagText             Tag = 0x41
	TagName             Tag = 0x42
	TagKeyword          Tag = 0x44
	TagURI              Tag = 0x45
	TagURIScheme        Tag = 0x46
	TagCharset          Tag = 0x47
	TagLanguage         Tag = 0x48
	TagMimeMediaType    Tag = 0x49
	TagMemberName       Tag = 0x4a
)

// IsOutOfBand reports a tag in 0x10-0x1f. The IANA "Out-of-Band Attribute Value Tags"
// registry reserves that whole range, and RFC 8010 section 3.8 says such a value is
// empty, so the decoder keeps no data for it (whatever its length).
func (t Tag) IsOutOfBand() bool { return t >= 0x10 && t <= 0x1f }

// fixedLength is the value-length RFC 8010 section 3.9 fixes for a tag, or -1.
func (t Tag) fixedLength() int {
	switch t {
	case TagInteger, TagEnum:
		return 4
	case TagBoolean:
		return 1
	case TagResolution:
		return 9
	case TagRangeOfInteger:
		return 8
	}
	return -1
}

// Resolution units.
const (
	UnitsDPI  = 3
	UnitsDPCM = 4
)

// Resolution is an IPP resolution value.
type Resolution struct {
	X, Y  int
	Units int // UnitsDPI or UnitsDPCM
}

// Range is an IPP rangeOfInteger value.
type Range struct{ Lower, Upper int }

// TextWithLanguage is a textWithLanguage or nameWithLanguage value.
type TextWithLanguage struct{ Language, Text string }

// Value is one attribute value with its wire tag, so a decoded message re-encodes byte
// for byte. Data is an int (integer, enum), bool, string (tags 0x40..0x5f), []byte
// (octetString, dateTime and anything unknown), Resolution, Range, TextWithLanguage,
// Collection, or nil for out-of-band values.
type Value struct {
	Tag  Tag
	Data any
}

// Attribute is a named attribute with one or more values.
type Attribute struct {
	Name   string
	Values []Value
}

// First returns the first value's data, or nil.
func (a *Attribute) First() any {
	if a == nil || len(a.Values) == 0 {
		return nil
	}
	return a.Values[0].Data
}

// Strings returns the text of every string and text-with-language value.
func (a *Attribute) Strings() []string {
	if a == nil {
		return nil
	}
	var out []string
	for _, v := range a.Values {
		switch d := v.Data.(type) {
		case string:
			out = append(out, d)
		case TextWithLanguage:
			out = append(out, d.Text)
		}
	}
	return out
}

// Ints returns every integer and enum value.
func (a *Attribute) Ints() []int {
	if a == nil {
		return nil
	}
	var out []int
	for _, v := range a.Values {
		if n, ok := v.Data.(int); ok {
			out = append(out, n)
		}
	}
	return out
}

// Collections returns every collection value.
func (a *Attribute) Collections() []Collection {
	if a == nil {
		return nil
	}
	var out []Collection
	for _, v := range a.Values {
		if c, ok := v.Data.(Collection); ok {
			out = append(out, c)
		}
	}
	return out
}

// Collection is an IPP collection value: its member attributes, in order.
type Collection []Attribute

// Get returns the named member, or nil.
func (c Collection) Get(name string) *Attribute {
	for i := range c {
		if c[i].Name == name {
			return &c[i]
		}
	}
	return nil
}

// First returns the first value of the named member, or nil.
func (c Collection) First(name string) any { return c.Get(name).First() }

// Group is an attribute group (operation, job, printer, unsupported…).
type Group struct {
	Tag        Tag
	Attributes []Attribute
}

// Get returns the named attribute, or nil.
func (g *Group) Get(name string) *Attribute {
	for i := range g.Attributes {
		if g.Attributes[i].Name == name {
			return &g.Attributes[i]
		}
	}
	return nil
}

// Message is an IPP request (Code = operation-id) or response (Code = status-code).
type Message struct {
	Major, Minor byte
	Code         int
	RequestID    int
	Groups       []Group
	Data         []byte // document bytes after the end-of-attributes tag
}

// NewMessage returns an IPP/1.1 message.
func NewMessage(code, requestID int, groups ...Group) *Message {
	return &Message{Major: 1, Minor: 1, Code: code, RequestID: requestID, Groups: groups}
}

// Group returns the first group with the tag, or nil.
func (m *Message) Group(tag Tag) *Group {
	for i := range m.Groups {
		if m.Groups[i].Tag == tag {
			return &m.Groups[i]
		}
	}
	return nil
}

// Lookup returns the attribute name from a group with the given tag, or nil. RFC 8011
// places every attribute in a defined group (operation, job, printer), so other groups
// are not searched.
func (m *Message) Lookup(group Tag, name string) *Attribute {
	for i := range m.Groups {
		if m.Groups[i].Tag != group {
			continue
		}
		if a := m.Groups[i].Get(name); a != nil {
			return a
		}
	}
	return nil
}

// --- constructors ---------------------------------------------------------------------

// Attr builds an attribute whose values all carry tag.
func Attr(name string, tag Tag, values ...any) Attribute {
	a := Attribute{Name: name, Values: make([]Value, len(values))}
	for i, v := range values {
		a.Values[i] = Value{Tag: tag, Data: v}
	}
	return a
}

func strAttr(name string, tag Tag, values []string) Attribute {
	vs := make([]any, len(values))
	for i, v := range values {
		vs[i] = v
	}
	return Attr(name, tag, vs...)
}

// Keyword builds a keyword (0x44) attribute.
func Keyword(name string, values ...string) Attribute { return strAttr(name, TagKeyword, values) }

// URI builds a uri (0x45) attribute.
func URI(name, value string) Attribute { return Attr(name, TagURI, value) }

// Charset builds a charset (0x47) attribute.
func Charset(name, value string) Attribute { return Attr(name, TagCharset, value) }

// Language builds a naturalLanguage (0x48) attribute.
func Language(name, value string) Attribute { return Attr(name, TagLanguage, value) }

// Name builds a nameWithoutLanguage (0x42) attribute.
func Name(name, value string) Attribute { return Attr(name, TagName, value) }

// Mime builds a mimeMediaType (0x49) attribute.
func Mime(name, value string) Attribute { return Attr(name, TagMimeMediaType, value) }

// Integer builds an integer (0x21) attribute.
func Integer(name string, values ...int) Attribute {
	vs := make([]any, len(values))
	for i, v := range values {
		vs[i] = v
	}
	return Attr(name, TagInteger, vs...)
}

// Enum builds an enum (0x23) attribute.
func Enum(name string, values ...int) Attribute {
	vs := make([]any, len(values))
	for i, v := range values {
		vs[i] = v
	}
	return Attr(name, TagEnum, vs...)
}

// NewCollection builds a collection (0x34) attribute with one value.
func NewCollection(name string, members ...Attribute) Attribute {
	return Attr(name, TagBeginCollection, Collection(members))
}

// --- encoding (RFC 8010 section 3) -------------------------------------------------------

// ErrFormat wraps every malformed-message error from Encode and Decode.
var ErrFormat = errors.New("ipp: malformed message")

func formatErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrFormat, fmt.Sprintf(format, args...))
}

// maxField is the largest name or value an RFC 8010 two-octet length can describe.
const maxField = 0xffff

type encoder struct {
	out []byte
	err error
}

func (e *encoder) fail(format string, args ...any) {
	if e.err == nil {
		e.err = formatErr(format, args...)
	}
}

func (e *encoder) length(n int) {
	if n < 0 || n > maxField {
		e.fail("a %d-octet field does not fit a two-octet length", n)
		n = 0
	}
	e.out = binary.BigEndian.AppendUint16(e.out, uint16(n))
}

func (e *encoder) text(s string) {
	e.length(len(s))
	e.out = append(e.out, s...)
}

func (e *encoder) int32(n int) { e.out = binary.BigEndian.AppendUint32(e.out, uint32(int32(n))) }

// value writes one attribute-with-one-value or additional-value (name "").
func (e *encoder) value(name string, v Value) {
	e.out = append(e.out, byte(v.Tag))
	e.text(name)
	switch d := v.Data.(type) {
	case Collection:
		// RFC 8010 section 3.1.6: an empty begCollection value, each member as a
		// memberAttrName value followed by its values (all with empty names), then an
		// endCollection with empty name and value.
		e.length(0)
		for _, m := range d {
			if len(m.Values) == 0 {
				e.fail("collection member %q needs at least one value", m.Name)
				return
			}
			e.out = append(e.out, byte(TagMemberName), 0, 0)
			e.text(m.Name)
			for _, mv := range m.Values {
				e.value("", mv)
			}
		}
		e.out = append(e.out, byte(TagEndCollection), 0, 0, 0, 0)
	case nil:
		e.length(0)
	case bool:
		e.length(1)
		var b byte
		if d {
			b = 1
		}
		e.out = append(e.out, b)
	case int:
		e.length(4)
		e.int32(d)
	case string:
		e.text(d)
	case []byte:
		e.length(len(d))
		e.out = append(e.out, d...)
	case Resolution:
		e.length(9)
		e.int32(d.X)
		e.int32(d.Y)
		e.out = append(e.out, byte(d.Units))
	case Range:
		e.length(8)
		e.int32(d.Lower)
		e.int32(d.Upper)
	case TextWithLanguage:
		e.length(4 + len(d.Language) + len(d.Text))
		e.text(d.Language)
		e.text(d.Text)
	default:
		e.fail("attribute %q: no wire form for a %T value", name, v.Data)
	}
}

// Encode serialises a message: version, operation or status code, request-id, the
// groups, the end-of-attributes-tag, then Data. A zero version is written as 1.1.
func Encode(m *Message) ([]byte, error) {
	major, minor := m.Major, m.Minor
	if major == 0 && minor == 0 {
		major, minor = 1, 1
	}
	e := &encoder{out: make([]byte, 0, 512+len(m.Data))}
	e.out = append(e.out, major, minor)
	e.out = binary.BigEndian.AppendUint16(e.out, uint16(m.Code))
	e.out = binary.BigEndian.AppendUint32(e.out, uint32(m.RequestID))
	for _, g := range m.Groups {
		e.out = append(e.out, byte(g.Tag))
		for _, a := range g.Attributes {
			if len(a.Values) == 0 {
				return nil, formatErr("attribute %q needs at least one value", a.Name)
			}
			e.value(a.Name, a.Values[0])
			for _, v := range a.Values[1:] {
				e.value("", v)
			}
		}
	}
	e.out = append(e.out, byte(TagEnd))
	e.out = append(e.out, m.Data...)
	if e.err != nil {
		return nil, e.err
	}
	return e.out, nil
}

// --- decoding ------------------------------------------------------------------------------

type reader struct {
	data []byte
	pos  int
}

func (r *reader) bytes(n int) ([]byte, error) {
	if n < 0 || n > len(r.data)-r.pos {
		return nil, formatErr("wanted %d octets at offset %d but only %d remain", n, r.pos, len(r.data)-r.pos)
	}
	b := r.data[r.pos : r.pos+n]
	r.pos += n
	return b, nil
}

// field reads a two-octet length and that many octets.
func (r *reader) field() ([]byte, error) {
	n, err := r.bytes(2)
	if err != nil {
		return nil, err
	}
	return r.bytes(int(binary.BigEndian.Uint16(n)))
}

// header reads a value-tag, name and value.
func (r *reader) header() (Tag, string, []byte, error) {
	t, err := r.bytes(1)
	if err != nil {
		return 0, "", nil, err
	}
	name, err := r.field()
	if err != nil {
		return 0, "", nil, err
	}
	raw, err := r.field()
	if err != nil {
		return 0, "", nil, err
	}
	return Tag(t[0]), string(name), raw, nil
}

// value turns a decoded header into a Value, reading a nested collection when the tag
// is begCollection.
func (r *reader) value(tag Tag, raw []byte) (Value, error) {
	if tag == TagBeginCollection {
		c, err := r.collection()
		return Value{Tag: tag, Data: c}, err
	}
	d, err := scalar(tag, raw)
	return Value{Tag: tag, Data: d}, err
}

func scalar(tag Tag, raw []byte) (any, error) {
	if tag.IsOutOfBand() {
		return nil, nil
	}
	if n := tag.fixedLength(); n >= 0 && len(raw) != n {
		return nil, formatErr("a 0x%02x value must be %d octets, not %d", byte(tag), n, len(raw))
	}
	be := binary.BigEndian
	i32 := func(b []byte) int { return int(int32(be.Uint32(b))) }
	switch tag {
	case TagInteger, TagEnum:
		return i32(raw), nil
	case TagBoolean:
		return raw[0] != 0, nil
	case TagResolution:
		return Resolution{X: i32(raw), Y: i32(raw[4:]), Units: int(raw[8])}, nil
	case TagRangeOfInteger:
		return Range{Lower: i32(raw), Upper: i32(raw[4:])}, nil
	case TagTextWithLanguage, TagNameWithLanguage:
		// RFC 8010 section 3.9: two length-prefixed strings that fill the value exactly.
		sub := &reader{data: raw}
		lang, err := sub.field()
		if err != nil {
			return nil, err
		}
		text, err := sub.field()
		if err != nil {
			return nil, err
		}
		if sub.pos != len(raw) {
			return nil, formatErr("a 0x%02x value has %d octets after its text", byte(tag), len(raw)-sub.pos)
		}
		return TextWithLanguage{Language: string(lang), Text: string(text)}, nil
	}
	if tag >= 0x40 && tag <= 0x5f { // character-string syntaxes, RFC 8010 Table 6
		return string(raw), nil
	}
	return append([]byte(nil), raw...), nil
}

// collection reads member attributes up to the matching endCollection. Member values
// carry empty names on the wire (RFC 8010 section 3.1.7); any name there is ignored.
func (r *reader) collection() (Collection, error) {
	var members Collection
	for {
		tag, _, raw, err := r.header()
		if err != nil {
			return nil, err
		}
		switch {
		case tag == TagEndCollection:
			return members, nil
		case tag == TagMemberName:
			members = append(members, Attribute{Name: string(raw)})
		case len(members) == 0:
			return nil, formatErr("a collection value arrived before its memberAttrName")
		default:
			v, err := r.value(tag, raw)
			if err != nil {
				return nil, err
			}
			last := &members[len(members)-1]
			last.Values = append(last.Values, v)
		}
	}
}

// Decode parses a message. Delimiter tags (below 0x10) open groups; everything after
// the end-of-attributes-tag is Data.
func Decode(data []byte) (*Message, error) {
	r := &reader{data: data}
	head, err := r.bytes(8)
	if err != nil {
		return nil, err
	}
	m := &Message{
		Major:     head[0],
		Minor:     head[1],
		Code:      int(binary.BigEndian.Uint16(head[2:])),
		RequestID: int(binary.BigEndian.Uint32(head[4:])),
	}
	var group *Group
	for {
		if r.pos == len(data) {
			return nil, formatErr("no end-of-attributes-tag before the end of the message")
		}
		if t := Tag(data[r.pos]); t < 0x10 {
			r.pos++
			if t == TagEnd {
				m.Data = data[r.pos:]
				return m, nil
			}
			m.Groups = append(m.Groups, Group{Tag: t})
			group = &m.Groups[len(m.Groups)-1]
			continue
		}
		if group == nil {
			return nil, formatErr("an attribute appears before the first delimiter tag")
		}
		tag, name, raw, err := r.header()
		if err != nil {
			return nil, err
		}
		v, err := r.value(tag, raw)
		if err != nil {
			return nil, err
		}
		if name != "" {
			group.Attributes = append(group.Attributes, Attribute{Name: name, Values: []Value{v}})
			continue
		}
		if len(group.Attributes) == 0 {
			return nil, formatErr("an additional value (empty name) opens a group")
		}
		last := &group.Attributes[len(group.Attributes)-1]
		last.Values = append(last.Values, v)
	}
}

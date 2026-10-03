// Package pdftest builds tiny PDF and DOCX files for tests and fixtures.
package pdftest

import (
	"bytes"
	"crypto/md5"
	"crypto/rc4"
	"fmt"
	"strings"
)

// Encryption modes the writer can emit.
const (
	EncryptNone         = iota // no encryption
	EncryptRestricted          // empty user password, owner restrictions only
	EncryptUserPassword        // a user password is required
	EncryptUnsupported         // a cipher version the reader does not support
)

const (
	defaultFontSize = 10.0
	firstChar       = 32
	lastChar        = 255
	euroByte        = 0x80
	rc4KeyBytes     = 5
	permissions     = -4
)

// helveticaWidths are the Helvetica advance widths for ASCII 32..126.
var helveticaWidths = [...]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
}

var passwordPad = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// Text is one string drawn at (X,Y) in points; Size defaults to 10.
type Text struct {
	X, Y float64
	Size float64
	S    string
}

// Page is a list of positioned strings.
type Page struct{ Texts []Text }

// Options controls optional PDF features.
type Options struct {
	Signed  bool
	Encrypt int
}

// Width returns the Helvetica width of s at the given size, in points.
func Width(s string, size float64) float64 {
	total := 0
	for _, r := range s {
		if r >= firstChar && r <= 126 {
			total += helveticaWidths[r-firstChar]
		} else {
			total += 556
		}
	}
	return float64(total) / 1000 * size
}

// Build returns PDF bytes with one page per entry.
func Build(pages []Page, opts Options) []byte {
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	var offsets []int
	obj := func(body string) int {
		offsets = append(offsets, buf.Len())
		id := len(offsets)
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", id, body)
		return id
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	kids := make([]string, len(pages))
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 4+2*i)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(pages), strings.Join(kids, " ")))
	obj(fontObject())
	enc := newCipher(opts.Encrypt)
	for i, p := range pages {
		pageID := 4 + 2*i
		contentID := pageID + 1
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents %d 0 R /Resources << /Font << /F1 3 0 R >> >> >>", contentID))
		stream := enc.apply(contentID, content(p))
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n<< /Length %d >>\nstream\n", contentID, len(stream))
		buf.Write(stream)
		buf.WriteString("\nendstream\nendobj\n")
	}
	trailerExtra := ""
	if opts.Signed {
		obj("<< /Type /Sig /Filter /Adobe.PPKLite /ByteRange [0 100 200 100] /Contents <00> >>")
	}
	if opts.Encrypt != EncryptNone {
		id := obj(enc.dict())
		trailerExtra = fmt.Sprintf(" /Encrypt %d 0 R /ID [<%x> <%x>]", id, enc.id, enc.id)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R%s >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, trailerExtra, xref)
	return buf.Bytes()
}

func fontObject() string {
	var w strings.Builder
	for c := firstChar; c <= lastChar; c++ {
		if c <= 126 {
			fmt.Fprintf(&w, "%d ", helveticaWidths[c-firstChar])
		} else {
			w.WriteString("556 ")
		}
	}
	return fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding /FirstChar %d /LastChar %d /Widths [%s] >>", firstChar, lastChar, strings.TrimSpace(w.String()))
}

func content(p Page) []byte {
	var b bytes.Buffer
	for _, t := range p.Texts {
		size := t.Size
		if size == 0 {
			size = defaultFontSize
		}
		fmt.Fprintf(&b, "BT /F1 %.2f Tf 1 0 0 1 %.2f %.2f Tm (", size, t.X, t.Y)
		for _, r := range t.S {
			switch {
			case r == '(' || r == ')' || r == '\\':
				b.WriteByte('\\')
				b.WriteByte(byte(r))
			case r == '€':
				b.WriteByte(euroByte)
			case r < 256:
				b.WriteByte(byte(r))
			default:
				b.WriteByte('?')
			}
		}
		b.WriteString(") Tj ET\n")
	}
	return b.Bytes()
}

// cipher encrypts content streams the way the reader decrypts them.
type cipher struct {
	mode int
	id   []byte
	o, u []byte
	key  []byte
}

func newCipher(mode int) *cipher {
	c := &cipher{mode: mode, id: bytes.Repeat([]byte{0xAB}, 16), o: bytes.Repeat([]byte{0x11}, 32)}
	if mode == EncryptNone {
		return c
	}
	h := md5.New()
	h.Write(passwordPad)
	h.Write(c.o)
	perm := int32(permissions)
	p := uint32(perm)
	h.Write([]byte{byte(p), byte(p >> 8), byte(p >> 16), byte(p >> 24)})
	h.Write(c.id)
	c.key = h.Sum(nil)[:rc4KeyBytes]
	rc, err := rc4.NewCipher(c.key)
	if err != nil {
		return c
	}
	c.u = append([]byte(nil), passwordPad...)
	rc.XORKeyStream(c.u, c.u)
	if mode == EncryptUserPassword {
		c.u = bytes.Repeat([]byte{0x22}, 32)
	}
	return c
}

func (c *cipher) dict() string {
	v := 1
	if c.mode == EncryptUnsupported {
		v = 5
	}
	return fmt.Sprintf("<< /Filter /Standard /V %d /R 2 /O <%x> /U <%x> /P %d >>", v, c.o, c.u, permissions)
}

// apply RC4-encrypts a stream for the given object number.
func (c *cipher) apply(objNum int, data []byte) []byte {
	if c.mode != EncryptRestricted {
		return data
	}
	h := md5.New()
	h.Write(c.key)
	h.Write([]byte{byte(objNum), byte(objNum >> 8), byte(objNum >> 16), 0, 0})
	rc, err := rc4.NewCipher(h.Sum(nil))
	if err != nil {
		return data
	}
	out := make([]byte, len(data))
	rc.XORKeyStream(out, data)
	return out
}

// Cell is a string placed at X on a row.
type Cell struct {
	X float64
	S string
}

// Add places cells on one row at height y.
func (p *Page) Add(y float64, cells ...Cell) {
	for _, c := range cells {
		p.Texts = append(p.Texts, Text{X: c.X, Y: y, S: c.S})
	}
}

// Right returns a cell whose right edge sits at rightX.
func Right(rightX float64, s string) Cell {
	return Cell{X: rightX - Width(s, defaultFontSize), S: s}
}

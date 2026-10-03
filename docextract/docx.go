package docextract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// DOCX safety limits and synthetic geometry.
const (
	maxDocxEntries      = 2000
	maxDocxUncompressed = 64 << 20 // total declared uncompressed bytes
	maxDocxXMLBytes     = 24 << 20 // document.xml read cap
	docxColumnWidth     = 100.0
	docxCharWidth       = 5.0
)

// ParseDOCX reads word/document.xml into rows. Paragraphs become one-word rows;
// table rows become rows whose words are cells at synthetic X positions.
func ParseDOCX(b []byte) ([]PageRows, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return nil, newInputError(FailCorrupt, fmt.Sprintf("zip: %v", err))
	}
	if len(zr.File) > maxDocxEntries {
		return nil, newInputError(FailTooLarge, fmt.Sprintf("%d archive entries exceeds the cap", len(zr.File)))
	}
	var total uint64
	var main *zip.File
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if total > maxDocxUncompressed {
			return nil, newInputError(FailTooLarge, "archive expands beyond the size cap")
		}
		if f.Name == docxMainPart {
			main = f
		}
	}
	if main == nil {
		return nil, newInputError(FailUnsupportedType, "word/document.xml missing")
	}
	rc, err := main.Open()
	if err != nil {
		return nil, newInputError(FailCorrupt, fmt.Sprintf("open document.xml: %v", err))
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(io.LimitReader(rc, maxDocxXMLBytes+1))
	if err != nil {
		return nil, newInputError(FailCorrupt, fmt.Sprintf("read document.xml: %v", err))
	}
	if len(data) > maxDocxXMLBytes {
		return nil, newInputError(FailTooLarge, "document.xml expands beyond the size cap")
	}
	rows, err := parseDocxXML(data)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, newInputError(FailEmpty, "document has no text")
	}
	return []PageRows{{Page: 1, Rows: rows}}, nil
}

// docxCell accumulates the paragraphs of one table cell.
type docxCell struct {
	col   int
	paras []string
	cur   strings.Builder
}

func (c *docxCell) endPara() {
	if t := strings.TrimSpace(c.cur.String()); t != "" {
		c.paras = append(c.paras, t)
	}
	c.cur.Reset()
}

// docxState tracks the streaming parse.
type docxState struct {
	rows     []Row
	tblDepth int
	para     strings.Builder
	inText   bool
	cells    []*docxCell
	cell     *docxCell
	col      int
}

func (s *docxState) addRow(words []Word) {
	if len(words) == 0 {
		return
	}
	s.rows = append(s.rows, Row{Y: -float64(len(s.rows)), Words: words})
}

func (s *docxState) endTableRow() {
	maxParas := 0
	for _, c := range s.cells {
		if len(c.paras) > maxParas {
			maxParas = len(c.paras)
		}
	}
	for k := 0; k < maxParas; k++ {
		var words []Word
		for _, c := range s.cells {
			if k < len(c.paras) {
				words = append(words, Word{X: float64(c.col) * docxColumnWidth, W: float64(len(c.paras[k])) * docxCharWidth, Text: c.paras[k]})
			}
		}
		s.addRow(words)
	}
	s.cells, s.cell, s.col = nil, nil, 0
}

func parseDocxXML(data []byte) ([]Row, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	s := &docxState{}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, newInputError(FailCorrupt, fmt.Sprintf("document.xml: %v", err))
		}
		switch t := tok.(type) {
		case xml.StartElement:
			s.start(t)
		case xml.EndElement:
			s.end(t)
		case xml.CharData:
			if s.inText {
				s.write(string(t))
			}
		}
	}
	return s.rows, nil
}

func (s *docxState) write(text string) {
	if s.cell != nil {
		s.cell.cur.WriteString(text)
		return
	}
	s.para.WriteString(text)
}

func (s *docxState) start(t xml.StartElement) {
	switch t.Name.Local {
	case "tbl":
		s.tblDepth++
	case "tr":
		if s.tblDepth == 1 {
			s.cells, s.col = nil, 0
		}
	case "tc":
		if s.tblDepth == 1 {
			s.cell = &docxCell{col: s.col}
			s.cells = append(s.cells, s.cell)
			s.col++
		}
	case "gridSpan":
		if s.tblDepth == 1 && s.cell != nil {
			for _, a := range t.Attr {
				if a.Name.Local == "val" {
					var n int
					if _, err := fmt.Sscanf(a.Value, "%d", &n); err == nil && n > 1 {
						s.col += n - 1
					}
				}
			}
		}
	case "t":
		s.inText = true
	case "tab":
		s.write(" ")
	case "br":
		if s.cell != nil {
			s.cell.endPara()
		} else {
			s.write(" ")
		}
	}
}

func (s *docxState) end(t xml.EndElement) {
	switch t.Name.Local {
	case "t":
		s.inText = false
	case "p":
		if s.cell != nil {
			s.cell.endPara()
		} else if s.tblDepth == 0 {
			if txt := strings.TrimSpace(s.para.String()); txt != "" {
				s.addRow([]Word{{X: 0, W: float64(len(txt)) * docxCharWidth, Text: txt}})
			}
			s.para.Reset()
		}
	case "tc":
		if s.tblDepth == 1 {
			s.cell = nil
		}
	case "tr":
		if s.tblDepth == 1 {
			s.endTableRow()
		}
	case "tbl":
		if s.tblDepth > 0 {
			s.tblDepth--
		}
	}
}

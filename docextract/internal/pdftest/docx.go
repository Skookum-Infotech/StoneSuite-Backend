package pdftest

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"strings"
)

// DocxBlock is a paragraph or a table (rows of cells of paragraphs).
type DocxBlock struct {
	Para  string
	Table [][][]string
}

const contentTypes = `<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`

const rootRels = `<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`

// BuildDocx returns a minimal .docx containing the blocks in order.
func BuildDocx(blocks []DocxBlock) []byte {
	var body strings.Builder
	for _, b := range blocks {
		if b.Table == nil {
			body.WriteString(para(b.Para))
			continue
		}
		body.WriteString("<w:tbl>")
		for _, row := range b.Table {
			body.WriteString("<w:tr>")
			for _, cell := range row {
				body.WriteString("<w:tc>")
				if len(cell) == 0 {
					body.WriteString(para(""))
				}
				for _, p := range cell {
					body.WriteString(para(p))
				}
				body.WriteString("</w:tc>")
			}
			body.WriteString("</w:tr>")
		}
		body.WriteString("</w:tbl>")
	}
	doc := `<?xml version="1.0" encoding="UTF-8"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() + `</w:body></w:document>`
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range []struct{ name, data string }{
		{"[Content_Types].xml", contentTypes}, {"_rels/.rels", rootRels}, {"word/document.xml", doc},
	} {
		w, err := zw.Create(f.name)
		if err != nil {
			return nil
		}
		if _, err := w.Write([]byte(f.data)); err != nil {
			return nil
		}
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

func para(text string) string {
	var esc bytes.Buffer
	if err := xml.EscapeText(&esc, []byte(text)); err != nil {
		return ""
	}
	return `<w:p><w:r><w:t xml:space="preserve">` + esc.String() + `</w:t></w:r></w:p>`
}

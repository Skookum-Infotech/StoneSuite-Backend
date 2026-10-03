package fabrication

import "time"

// MeasuredPiece records one required output on an on-site template.
type MeasuredPiece struct {
	Name        string  `json:"name"`
	LengthMM    float64 `json:"lengthMm"`
	WidthMM     float64 `json:"widthMm"`
	ThicknessMM float64 `json:"thicknessMm"`
}

// TemplateLine snapshots the commercial requirement and measured physical outputs.
type TemplateLine struct {
	SourceLineID string          `json:"sourceLineId"`
	MaterialID   string          `json:"materialId"`
	Finish       string          `json:"finish,omitempty"`
	Quantity     float64         `json:"quantity"`
	UnitPrice    float64         `json:"unitPrice"`
	Scope        string          `json:"scope"`
	Pieces       []MeasuredPiece `json:"pieces"`
}

// TemplateChange determines the approvals required by an immutable revision.
type TemplateChange struct {
	InternalRequired bool     `json:"internalRequired"`
	CustomerRequired bool     `json:"customerRequired"`
	ChangedLines     []string `json:"changedLines"`
}

// TemplateRevision preserves submitted measurements and their original order version.
type TemplateRevision struct {
	AvailableActions  []AvailableAction `json:"availableActions,omitempty"`
	JobVersion        int64             `json:"jobVersion"`
	ID                string            `json:"id"`
	Revision          int               `json:"revision"`
	SalesOrderVersion int64             `json:"salesOrderVersion"`
	State             string            `json:"state"`
	Baseline          []TemplateLine    `json:"baseline"`
	Lines             []TemplateLine    `json:"lines"`
	Change            TemplateChange    `json:"change"`
	CreatedAt         time.Time         `json:"createdAt"`
}

// SubmitTemplateInput creates a new immutable revision; revisions are never overwritten.
type SubmitTemplateInput struct {
	CommandMeta
	SalesOrderVersion int64          `json:"salesOrderVersion"`
	Lines             []TemplateLine `json:"lines"`
}

package docpdf

import "testing"

func TestSizeText(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	n := func(v int) *int { return &v }
	tests := []struct {
		name          string
		length, width *float64
		slabs         *int
		unit          string
		want          string
	}{
		{"nothing", nil, nil, nil, "cm", ""},
		{"size and slabs", f(300), f(150), n(2), "cm", "300 × 150 cm · 2 slabs"},
		{"inches", f(120), f(60), n(2), "in", "120 × 60 in · 2 slabs"},
		{"feet", f(10), f(5), n(1), "ft", "10 × 5 ft · 1 slab"},
		{"one slab is singular", f(300), f(150), n(1), "cm", "300 × 150 cm · 1 slab"},
		{"size without a count", f(304.8), f(152.4), nil, "cm", "304.8 × 152.4 cm"},
		{"a missing unit reads as inches", f(120), f(60), nil, "", "120 × 60 in"},
		{"count without a size", nil, nil, n(4), "in", "4 slabs"},
		{"a lone dimension is not printed", f(300), nil, nil, "cm", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SizeText(tt.length, tt.width, tt.slabs, tt.unit); got != tt.want {
				t.Errorf("SizeText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDrawLineTable_SizeLineAddsOneDescriptionRow(t *testing.T) {
	plain, _ := endY(PrintableDoc{Lines: []PrintLine{kitLine(""), kitLine("")}})
	sized := kitLine("")
	sized.Size = "300 × 150 cm · 2 slabs"
	got, _ := endY(PrintableDoc{Lines: []PrintLine{sized, sized}})

	if want := plain + 2*descLineH; got < want-0.001 || got > want+0.001 {
		t.Errorf("sized rows end at %v, want %v (one %vmm line per row)", got, want, descLineH)
	}
}

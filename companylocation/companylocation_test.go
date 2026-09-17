package companylocation

import "testing"

func TestValidate(t *testing.T) {
	base := func() Input {
		return Input{Name: "Main Office"}
	}
	tooLong := func() string {
		b := make([]byte, MaxFieldLength+1)
		for i := range b {
			b[i] = 'a'
		}
		return string(b)
	}()
	atMax := tooLong[:MaxFieldLength]

	tests := []struct {
		name    string
		mutate  func(in *Input)
		wantErr bool
	}{
		{"valid minimal", func(in *Input) {}, false},
		{"name empty", func(in *Input) { in.Name = "" }, true},
		{"name whitespace only", func(in *Input) { in.Name = "   " }, true},
		{"name at max length", func(in *Input) { in.Name = atMax }, false},
		{"name too long", func(in *Input) { in.Name = tooLong }, true},
		{"phone too long", func(in *Input) { in.Phone = tooLong }, true},

		{"valid full address", func(in *Input) {
			in.Address = Address{
				Line1: "123 Main St", Line2: "Building B", Suite: "400",
				City: "Springfield", Country: "United States", State: "IL", Zip: "62704",
			}
		}, false},
		{"address line1 too long", func(in *Input) { in.Address.Line1 = tooLong }, true},
		{"address line2 too long", func(in *Input) { in.Address.Line2 = tooLong }, true},
		{"address suite too long", func(in *Input) { in.Address.Suite = tooLong }, true},
		{"address city too long", func(in *Input) { in.Address.City = tooLong }, true},
		{"address country too long", func(in *Input) { in.Address.Country = tooLong }, true},
		{"address state too long", func(in *Input) { in.Address.State = tooLong }, true},
		{"address zip too long", func(in *Input) { in.Address.Zip = tooLong }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base()
			tt.mutate(&in)
			err := Validate(in)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%+v) error = %v, wantErr %v", in, err, tt.wantErr)
			}
		})
	}
}

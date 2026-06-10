package workflow

import "testing"

func TestValidateNumberingConfig(t *testing.T) {
	base := func() NumberingConfig {
		return NumberingConfig{WorkflowID: "wf-1", Enabled: true, Prefix: "LEAD-", Suffix: "", MinDigits: 4, NextNumber: 1}
	}

	tests := []struct {
		name    string
		mutate  func(c *NumberingConfig)
		wantErr bool
	}{
		{"valid default", func(c *NumberingConfig) {}, false},
		{"min digits too low", func(c *NumberingConfig) { c.MinDigits = 0 }, true},
		{"min digits too high", func(c *NumberingConfig) { c.MinDigits = 11 }, true},
		{"min digits at floor", func(c *NumberingConfig) { c.MinDigits = MinDigitsFloor }, false},
		{"min digits at ceil", func(c *NumberingConfig) { c.MinDigits = MinDigitsCeil }, false},
		{"next number zero", func(c *NumberingConfig) { c.NextNumber = 0 }, true},
		{"next number negative", func(c *NumberingConfig) { c.NextNumber = -5 }, true},
		{"prefix too long", func(c *NumberingConfig) { c.Prefix = "this-prefix-is-way-too-long-for-use" }, true},
		{"suffix too long", func(c *NumberingConfig) { c.Suffix = "this-suffix-is-way-too-long-for-use" }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(&cfg)
			err := ValidateNumberingConfig(cfg)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateNumberingConfig(%+v) error = %v, wantErr %v", cfg, err, tt.wantErr)
			}
		})
	}
}

func TestFormatRecordNumber(t *testing.T) {
	tests := []struct {
		name      string
		prefix    string
		suffix    string
		minDigits int
		n         int64
		want      string
	}{
		{"prefix and padding", "LEAD-", "", 4, 1, "LEAD-0001"},
		{"prefix and suffix", "LEAD-", "-A", 4, 12, "LEAD-0012-A"},
		{"no prefix or suffix", "", "", 1, 5, "5"},
		{"value exceeds min digits", "VE", "", 2, 1234, "VE1234"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatRecordNumber(tt.prefix, tt.suffix, tt.minDigits, tt.n)
			if got != tt.want {
				t.Errorf("formatRecordNumber(%q,%q,%d,%d) = %q, want %q",
					tt.prefix, tt.suffix, tt.minDigits, tt.n, got, tt.want)
			}
		})
	}
}

package controllers

import "testing"

func TestValidateAttachFile_DXF(t *testing.T) {
	cases := []struct {
		name        string
		fileName    string
		contentType string
		wantErr     bool
	}{
		{"canonical dxf", "SO_9073_Kitchen_P1.dxf", "application/dxf", false},
		{"uppercase extension", "KITCHEN.DXF", "application/dxf", false},
		{"dxf type on a pdf name", "plan.pdf", "application/dxf", true},
		{"pdf type on a dxf name", "plan.dxf", "application/pdf", true},
		{"empty type browsers report for dxf", "plan.dxf", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAttachFile(tc.fileName, tc.contentType, 1024)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateAttachFile(%q, %q) error = %v, wantErr %v", tc.fileName, tc.contentType, err, tc.wantErr)
			}
		})
	}
}

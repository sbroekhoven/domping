package input

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		reduced bool
		wantErr bool
	}{
		{in: "example.com", want: "example.com"},
		{in: "  Example.COM.  ", want: "example.com"},
		{in: "example.com..", want: "example.com"},
		{in: "www.example.com", want: "example.com", reduced: true},
		{in: "a.b.c.example.co.uk", want: "example.co.uk", reduced: true},
		{in: "example.co.uk", want: "example.co.uk"},
		{in: "foo.blogspot.com", want: "blogspot.com", reduced: true}, // private suffixes are ignored
		{in: "bücher.de", want: "xn--bcher-kva.de", reduced: true},
		{in: "xn--bcher-kva.de", want: "xn--bcher-kva.de"},
		{in: "例え.jp", want: "xn--r8jz45g.jp", reduced: true},
		{in: "example.unknowntld", want: "example.unknowntld"},
		{in: "my-site.nl", want: "my-site.nl"},

		{in: "", wantErr: true},
		{in: "   ", wantErr: true},
		{in: "localhost", wantErr: true},
		{in: "com", wantErr: true},
		{in: "co.uk", wantErr: true},
		{in: "http://example.com", wantErr: true},
		{in: "example.com/path", wantErr: true},
		{in: "user@example.com", wantErr: true},
		{in: "example.com:80", wantErr: true},
		{in: "exa mple.com", wantErr: true},
		{in: "-example.com", wantErr: true},
		{in: "example-.com", wantErr: true},
		{in: "exa_mple.com", wantErr: true},
		{in: "example..com", wantErr: true},
		{in: strings.Repeat("a", 64) + ".com", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := Normalize(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Normalize(%q) = %q, want error", tt.in, got.Name)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize(%q) error: %v", tt.in, err)
			}
			if got.Name != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q", tt.in, got.Name, tt.want)
			}
			if got.Reduced() != tt.reduced {
				t.Errorf("Normalize(%q).Reduced() = %v, want %v", tt.in, got.Reduced(), tt.reduced)
			}
		})
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     []string
		errLines []int
	}{
		{
			name:  "comments blanks and whitespace",
			input: "# header\n\nexample.com\n   \n  # indented comment\n\tEXAMPLE.org.  \n",
			want:  []string{"example.com", "example.org"},
		},
		{
			name:  "deduplicates after normalization",
			input: "example.com\nwww.example.com\nExample.com.\nexample.net\n",
			want:  []string{"example.com", "example.net"},
		},
		{
			name:     "reports invalid lines with line numbers",
			input:    "example.com\nnot a domain\nco.uk\nexample.net\n",
			want:     []string{"example.com", "example.net"},
			errLines: []int{2, 3},
		},
		{
			name:  "windows line endings",
			input: "example.com\r\nexample.org\r\n",
			want:  []string{"example.com", "example.org"},
		},
		{
			name:  "empty",
			input: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, errs := Parse(strings.NewReader(tt.input))
			var names []string
			for _, d := range got {
				names = append(names, d.Name)
			}
			if strings.Join(names, ",") != strings.Join(tt.want, ",") {
				t.Errorf("domains = %v, want %v", names, tt.want)
			}
			if len(errs) != len(tt.errLines) {
				t.Fatalf("got %d errors (%v), want %d", len(errs), errs, len(tt.errLines))
			}
			for i, err := range errs {
				var le *LineError
				if !errors.As(err, &le) || le.Line != tt.errLines[i] {
					t.Errorf("error %d = %v, want line %d", i, err, tt.errLines[i])
				}
			}
		})
	}
}

func TestDedupe(t *testing.T) {
	in := []Domain{{Name: "a.com"}, {Name: "b.com"}, {Name: "a.com"}}
	got := Dedupe(in)
	if len(got) != 2 || got[0].Name != "a.com" || got[1].Name != "b.com" {
		t.Errorf("Dedupe = %v", got)
	}
}

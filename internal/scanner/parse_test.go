package scanner

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePorts(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    []int
		wantErr bool
	}{
		{name: "empty falls back to 80", spec: "", want: []int{80}},
		{name: "single", spec: "443", want: []int{443}},
		{name: "list sorted and de-duplicated", spec: "443,80,443", want: []int{80, 443}},
		{name: "range", spec: "8000-8003", want: []int{8000, 8001, 8002, 8003}},
		{name: "reversed range", spec: "8003-8000", want: []int{8000, 8001, 8002, 8003}},
		{name: "mixed separators", spec: "80, 443;8080 8443-8444", want: []int{80, 443, 8080, 8443, 8444}},
		{name: "full width comma", spec: "80\uFF0C443", want: []int{80, 443}},
		{name: "non numeric", spec: "http", wantErr: true},
		{name: "above range", spec: "70000", wantErr: true},
		{name: "zero", spec: "0", wantErr: true},
		{name: "range too large", spec: "1-65535", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePorts(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParsePorts(%q) = %v, want error", tc.spec, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePorts(%q) returned error: %v", tc.spec, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ParsePorts(%q) = %v, want %v", tc.spec, got, tc.want)
			}
		})
	}
}

func TestParseTargets(t *testing.T) {
	t.Run("shared port list", func(t *testing.T) {
		targets, warnings, err := ParseTargets("example.com\n192.168.1.1\n", "80,443")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(warnings) != 0 {
			t.Fatalf("unexpected warnings: %v", warnings)
		}
		want := []Target{
			{Host: "example.com", Port: 80},
			{Host: "example.com", Port: 443},
			{Host: "192.168.1.1", Port: 80},
			{Host: "192.168.1.1", Port: 443},
		}
		if !reflect.DeepEqual(targets, want) {
			t.Fatalf("targets = %+v, want %+v", targets, want)
		}
	})

	t.Run("inline port overrides shared list", func(t *testing.T) {
		targets, _, err := ParseTargets("example.com:8080", "80,443")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []Target{{Host: "example.com", Port: 8080}}
		if !reflect.DeepEqual(targets, want) {
			t.Fatalf("targets = %+v, want %+v", targets, want)
		}
	})

	t.Run("comments, blank lines and duplicates", func(t *testing.T) {
		targets, _, err := ParseTargets("# primary\n\nexample.com\nexample.com 443\nEXAMPLE.com:443 # dup\n", "443")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []Target{{Host: "example.com", Port: 443}}
		if !reflect.DeepEqual(targets, want) {
			t.Fatalf("targets = %+v, want %+v", targets, want)
		}
	})

	t.Run("ipv6 forms", func(t *testing.T) {
		targets, _, err := ParseTargets("[::1]:443\n[2001:db8::1]\nfe80::1", "80")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []Target{
			{Host: "::1", Port: 443},
			{Host: "2001:db8::1", Port: 80},
			{Host: "fe80::1", Port: 80},
		}
		if !reflect.DeepEqual(targets, want) {
			t.Fatalf("targets = %+v, want %+v", targets, want)
		}
	})

	t.Run("invalid line reports line number", func(t *testing.T) {
		_, _, err := ParseTargets("example.com\nbad-host:notaport\n", "80")
		if err == nil {
			t.Fatal("expected an error")
		}
		if want := "第 2 行"; !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err.Error(), want)
		}
	})

	t.Run("empty input", func(t *testing.T) {
		if _, _, err := ParseTargets("   \n\n", "80"); err == nil {
			t.Fatal("expected an error for empty input")
		}
	})
}

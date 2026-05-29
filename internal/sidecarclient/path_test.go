package sidecarclient

import "testing"

func TestSidecarPathJoinsStorageMountAndRemotePath(t *testing.T) {
	tests := []struct {
		name  string
		mount string
		elems []string
		want  string
	}{
		{name: "absolute remote", mount: "/115-main", elems: []string{"/Movies", "Film.mkv"}, want: "/115-main/Movies/Film.mkv"},
		{name: "relative remote", mount: "/115-main", elems: []string{"Movies", "Film.mkv"}, want: "/115-main/Movies/Film.mkv"},
		{name: "root mount", mount: "/", elems: []string{"/Movies", "Film.mkv"}, want: "/Movies/Film.mkv"},
		{name: "no mount", elems: []string{"/Movies", "Film.mkv"}, want: "/Movies/Film.mkv"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sidecarPath(tt.mount, tt.elems...); got != tt.want {
				t.Fatalf("sidecarPath = %q, want %q", got, tt.want)
			}
		})
	}
}

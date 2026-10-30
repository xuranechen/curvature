package api

import (
	"strings"
	"testing"
)

func TestApplyUITitle(t *testing.T) {
	const base = `<!doctype html><html><head><title>Curvature</title></head><body>hi</body></html>`

	tests := []struct {
		name      string
		title     string
		want      string
		untouched bool
	}{
		{name: "default keeps html as-is", title: "Curvature", untouched: true},
		{name: "empty keeps html as-is", title: "", untouched: true},
		{name: "blank spaces keep html as-is", title: "   ", untouched: true},
		{name: "custom title replaced", title: "Curvature-TX", want: "<title>Curvature-TX</title>"},
		{name: "title is html-escaped", title: `Curvature <b>&"`, want: `<title>Curvature &lt;b&gt;&amp;&#34;</title>`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &HTTPHandler{UITitle: tc.title}
			got := string(h.applyUITitle([]byte(base)))
			if tc.untouched {
				if got != base {
					t.Fatalf("expected untouched html, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("expected %q in output, got %q", tc.want, got)
			}
			if strings.Count(got, "<title>") != 1 {
				t.Fatalf("expected exactly one title tag, got %q", got)
			}
		})
	}
}

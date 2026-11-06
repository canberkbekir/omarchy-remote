package web

import (
	"strings"
	"testing"
)

func TestPagesHaveNoInlineScripts(t *testing.T) {
	for _, name := range []string{"index.html", "join.html"} {
		data, err := Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, tag := range strings.Split(string(data), "<script")[1:] {
			if !strings.HasPrefix(tag, ` src="`) || !strings.HasPrefix(tag[strings.Index(tag, ">"):], "></script>") {
				t.Errorf("%s has an inline script, which the CSP blocks", name)
			}
		}
		if strings.Contains(string(data), "style=") {
			t.Errorf("%s has a style attribute, which the CSP blocks", name)
		}
	}
}

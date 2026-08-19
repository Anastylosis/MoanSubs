package subtitle

import "testing"

func TestBaseLang(t *testing.T) {
	cases := []struct {
		name    string
		tag     string
		want    string
		wantErr bool
	}{
		{"regional tag loses its region", "pt-BR", "pt", false},
		{"bare tag round-trips", "en", "en", false},
		{"garbage is rejected, not guessed at", "not a tag!!", "", true},
		{"empty tag is rejected", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := BaseLang(c.tag)
			if c.wantErr {
				if err == nil {
					t.Fatalf("BaseLang(%q) = %q, nil; want an error", c.tag, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("BaseLang(%q): unexpected error: %v", c.tag, err)
			}
			if got != c.want {
				t.Errorf("BaseLang(%q) = %q, want %q", c.tag, got, c.want)
			}
		})
	}
}

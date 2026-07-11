package main

import (
	"reflect"
	"testing"
)

// [export] inherits [project]'s allowlists so one policy covers both
// public surfaces, and overrides them when it says so. The nil/empty distinction
// is the whole mechanism: TOML gives an absent key a nil slice and `x = []` an
// empty non-nil one, and only the first inherits.
func TestInheritList(t *testing.T) {
	project := []string{"cover", "rating"}
	cases := []struct {
		name string
		own  []string
		want []string
	}{
		{"absent inherits", nil, project},
		{"set overrides", []string{"cover"}, []string{"cover"}},
		{"explicitly empty declines to inherit", []string{}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := inheritList(tc.own, project); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("inheritList(%v, %v) = %v, want %v", tc.own, project, got, tc.want)
			}
		})
	}
}

// An empty list yields a nil set, which export reads as "no allowlist, keep
// everything". A configured list yields the set. This is what makes
// `[export] public-extras = []` mean "publish every extra in the download", the
// same as never configuring one, rather than "publish none of them".
func TestAllowlist(t *testing.T) {
	if got := allowlist(nil); got != nil {
		t.Errorf("allowlist(nil) = %v, want nil (keep everything)", got)
	}
	if got := allowlist([]string{}); got != nil {
		t.Errorf("allowlist([]) = %v, want nil (keep everything)", got)
	}
	want := map[string]bool{"cover": true, "rating": true}
	if got := allowlist([]string{"cover", "rating"}); !reflect.DeepEqual(got, want) {
		t.Errorf("allowlist = %v, want %v", got, want)
	}
}

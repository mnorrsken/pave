package main

import "testing"

func TestUTF8CType(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"LANG": "C"}, "C.UTF-8"},
		{map[string]string{"LANG": "POSIX"}, "C.UTF-8"},
		{map[string]string{"LC_CTYPE": "C", "LANG": "en_US.UTF-8"}, "C.UTF-8"},
		{map[string]string{"LC_CTYPE": "en_US.UTF-8", "LANG": "C"}, ""},
		{map[string]string{"LANG": "en_US.UTF-8"}, ""},
		{map[string]string{"LANG": "de_DE.ISO-8859-1"}, ""},
		{map[string]string{"LC_ALL": "C"}, ""},
		{map[string]string{}, ""},
	} {
		got, ok := utf8CType(func(k string) string { return tc.env[k] })
		if got != tc.want || ok != (tc.want != "") {
			t.Errorf("%v: got %q, %v, want %q", tc.env, got, ok, tc.want)
		}
	}
}

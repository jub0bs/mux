package mux

import (
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func Test_newRouteRegexp_Errors(t *testing.T) {
	tests := []struct {
		in, out string
	}{
		{"/{}", `mux: missing name or pattern in "{}"`},
		{"/{:.}", `mux: missing name or pattern in "{:.}"`},
		{"/{a:}", `mux: missing name or pattern in "{a:}"`},
		{"/{id:abc(}", `mux: error compiling regex for "{id:abc(}":`},
	}

	for _, tc := range tests {
		t.Run("Test case for "+tc.in, func(t *testing.T) {
			_, err := newRouteRegexp(tc.in, 0, routeRegexpOptions{})
			if err != nil {
				if strings.HasPrefix(err.Error(), tc.out) {
					return
				}
				t.Errorf("Resulting error does not contain %q as expected, error: %s", tc.out, err)
			} else {
				t.Error("Expected error, got nil")
			}
		})
	}
}

func Test_findFirstQueryKey(t *testing.T) {
	tests := []string{
		"a=1&b=2",
		"a=1&a=2&a=banana",
		"ascii=%3Ckey%3A+0x90%3E",
		"a=1;b=2",
		"a=1&a=2;a=banana",
		"a==",
		"a=%2",
		"a=20&%20%3F&=%23+%25%21%3C%3E%23%22%7B%7D%7C%5C%5E%5B%5D%60%E2%98%BA%09:%2F@$%27%28%29%2A%2C%3B&a=30",
		"a=1& ?&=#+%!<>#\"{}|\\^[]`☺\t:/@$'()*,;&a=5",
		"a=xxxxxxxxxxxxxxxx&b=YYYYYYYYYYYYYYY&c=ppppppppppppppppppp&f=ttttttttttttttttt&a=uuuuuuuuuuuuu",
	}
	for _, query := range tests {
		t.Run(query, func(t *testing.T) {
			// Check against url.ParseQuery, ignoring the error.
			all, _ := url.ParseQuery(query)
			for key, want := range all {
				t.Run(key, func(t *testing.T) {
					got, ok := findFirstQueryKey(query, key, false)
					if !ok {
						t.Error("Did not get expected key", key)
					}
					if !reflect.DeepEqual(got, want[0]) {
						t.Errorf("findFirstQueryKey(%s,%s) = %v, want %v", query, key, got, want[0])
					}
				})
			}
		})
	}
}

func Benchmark_findQueryKey(b *testing.B) {
	tests := []string{
		"a=1&b=2",
		"ascii=%3Ckey%3A+0x90%3E",
		"a=20&%20%3F&=%23+%25%21%3C%3E%23%22%7B%7D%7C%5C%5E%5B%5D%60%E2%98%BA%09:%2F@$%27%28%29%2A%2C%3B&a=30",
		"a=xxxxxxxxxxxxxxxx&bbb=YYYYYYYYYYYYYYY&cccc=ppppppppppppppppppp&ddddd=ttttttttttttttttt&a=uuuuuuuuuuuuu",
		"a=;b=;c=;d=;e=;f=;g=;h=;i=,j=;k=",
	}
	for i, query := range tests {
		b.Run(strconv.Itoa(i), func(b *testing.B) {
			// Check against url.ParseQuery, ignoring the error.
			all, _ := url.ParseQuery(query)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for key := range all {
					_, _ = findFirstQueryKey(query, key, false)
				}
			}
		})
	}
}

func Benchmark_findQueryKeyGoLib(b *testing.B) {
	tests := []string{
		"a=1&b=2",
		"ascii=%3Ckey%3A+0x90%3E",
		"a=20&%20%3F&=%23+%25%21%3C%3E%23%22%7B%7D%7C%5C%5E%5B%5D%60%E2%98%BA%09:%2F@$%27%28%29%2A%2C%3B&a=30",
		"a=xxxxxxxxxxxxxxxx&bbb=YYYYYYYYYYYYYYY&cccc=ppppppppppppppppppp&ddddd=ttttttttttttttttt&a=uuuuuuuuuuuuu",
		"a=;b=;c=;d=;e=;f=;g=;h=;i=,j=;k=",
	}
	for i, query := range tests {
		b.Run(strconv.Itoa(i), func(b *testing.B) {
			// Check against url.ParseQuery, ignoring the error.
			all, _ := url.ParseQuery(query)
			var u url.URL
			u.RawQuery = query
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for key := range all {
					v := u.Query()[key]
					if len(v) > 0 {
						_ = v[0]
					}
				}
			}
		})
	}
}

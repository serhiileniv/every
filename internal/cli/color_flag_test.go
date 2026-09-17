package cli

import (
	"reflect"
	"testing"

	"github.com/serhiileniv/every/internal/ui"
)

// --color is stripped from every's own half of argv and from nowhere else.
//
// The case that cannot go in the surface table: `every 15m -- mycmd --color=x`
// registers a real task, so the table's hermeticity guard rejects it. It still
// has to be asserted somewhere -- swallowing a flag meant for the user's own
// program would corrupt the command we then schedule.
func TestApplyColorFlag(t *testing.T) {
	for _, tc := range []struct {
		name    string
		argv    []string
		want    []string
		wantErr bool
		mode    ui.Mode
	}{
		{
			name: "stripped from the flag half",
			argv: []string{"list", "--color=never"},
			want: []string{"list"},
			mode: ui.ModeNever,
		},
		{
			name: "separate value form",
			argv: []string{"list", "--color", "always"},
			want: []string{"list"},
			mode: ui.ModeAlways,
		},
		{
			name: "left alone after --",
			argv: []string{"15m", "--", "mycmd", "--color=bogus"},
			want: []string{"15m", "--", "mycmd", "--color=bogus"},
		},
		{
			name: "stripped before -- but not after",
			argv: []string{"15m", "--color=never", "--", "mycmd", "--color=always"},
			want: []string{"15m", "--", "mycmd", "--color=always"},
			mode: ui.ModeNever,
		},
		{
			name:    "bad value is a usage error",
			argv:    []string{"list", "--color=bogus"},
			wantErr: true,
		},
		{
			name:    "missing value is a usage error",
			argv:    []string{"list", "--color"},
			wantErr: true,
		},
		{
			name: "absent leaves argv untouched",
			argv: []string{"list", "--json"},
			want: []string{"list", "--json"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got ui.Mode
			c := &CLI{Recolor: func(m ui.Mode) ui.Color {
				got = m
				return ui.Color{}
			}}

			rest, err := c.applyColorFlag(tc.argv)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("applyColorFlag(%v) = nil error, want usage error", tc.argv)
				}
				return
			}
			if err != nil {
				t.Fatalf("applyColorFlag(%v): %v", tc.argv, err)
			}
			if !reflect.DeepEqual(rest, tc.want) {
				t.Errorf("argv = %v, want %v", rest, tc.want)
			}
			if got != tc.mode {
				t.Errorf("mode = %v, want %v", got, tc.mode)
			}
		})
	}
}

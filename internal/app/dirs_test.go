package app

import "testing"

func TestUserDirs(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Dirs
	}{
		{
			"defaults",
			map[string]string{"HOME": "/home/u"},
			Dirs{Home: "/home/u", User: "/home/u/.config/loom", State: "/home/u/.local/state/loom"},
		},
		{
			"xdg",
			map[string]string{"HOME": "/home/u", "XDG_CONFIG_HOME": "/cfg", "XDG_STATE_HOME": "/state"},
			Dirs{Home: "/home/u", User: "/cfg/loom", State: "/state/loom"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := UserDirs(func(k string) string { return tt.env[k] }); got != tt.want {
				t.Errorf("UserDirs = %+v, want %+v", got, tt.want)
			}
		})
	}
}

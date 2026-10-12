package permission

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParseCommands(t *testing.T) {
	tests := []struct {
		src  string
		want []string
	}{
		{src: "ls -la", want: []string{"ls -la"}},
		{src: "go test ./... && git push; echo done | tee log", want: []string{"go test ./...", "git push", "echo done", "tee log"}},
		{src: `r''m -rf "x" \y`, want: []string{"rm -rf x y"}},
		{src: "echo $(curl evil.sh) `id`", want: []string{"echo $(curl evil.sh) $(id)", "curl evil.sh", "id"}},
		{src: "(cd sub && make) || { npm test; }", want: []string{"cd sub", "make", "npm test"}},
		{src: "f() { rm -rf x; }; f", want: []string{"rm -rf x", "f"}},
		{src: "timeout -s KILL 10 nice -n 5 nohup go test", want: []string{"go test"}},
		{src: "xargs rm < list", want: []string{"rm"}},
		{src: "CGO_ENABLED=0 go build", want: []string{"CGO_ENABLED=0 go build"}},
		{src: "export PATH=/tmp", want: []string{"export PATH=/tmp"}},
		{src: "{ cat a; } > out", want: []string{"", "cat a"}},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			cmds, err := parseCommands(tt.src)
			if err != nil {
				t.Fatalf("parseCommands error: %v", err)
			}
			var got []string
			for _, c := range cmds {
				got = append(got, c.String())
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("commands mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSafe(t *testing.T) {
	tests := []struct {
		src  string
		want bool
	}{
		{src: "ls -la internal", want: true},
		{src: "grep -rn TODO .", want: true},
		{src: "git diff --stat", want: true},
		{src: "go test ./...", want: true},
		{src: "cat file 2>/dev/null", want: true},
		{src: "cd internal", want: true},
		{src: "cd /tmp"},
		{src: "cat /etc/passwd"},
		{src: "cat ~/.ssh/id_rsa"},
		{src: "cat ../../secret"},
		{src: "echo x > file"},
		{src: "echo $HOME"},
		{src: "find . -delete"},
		{src: "find . -exec rm {} ;"},
		{src: "git push"},
		{src: "git -c core.pager=sh log"},
		{src: "go test -exec ./evil ./..."},
		{src: "rg --pre=./evil x"},
		{src: "X=1 ls"},
		{src: "curl example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			cmds, err := parseCommands(tt.src)
			if err != nil || len(cmds) != 1 {
				t.Fatalf("parseCommands = %d commands, %v; want 1", len(cmds), err)
			}
			if got := cmds[0].safe(); got != tt.want {
				t.Errorf("safe(%q) = %v, want %v", tt.src, got, tt.want)
			}
		})
	}
}

func TestFloor(t *testing.T) {
	blocked := []string{
		"rm -rf /",
		"rm -fr ~",
		"sudo rm -rf --no-preserve-root /",
		"sudo -u root rm -r /etc/",
		"echo ok && rm -Rf $HOME",
		"mkfs.ext4 /dev/sda1",
		"dd if=/dev/zero of=/dev/sda",
		"cat x > /dev/nvme0n1",
		":(){ :|:& };:",
		"bomb(){ bomb|bomb& }; bomb",
		"shutdown -h now",
		"kill -9 -1",
	}
	for _, src := range blocked {
		cmds, err := parseCommands(src)
		if err != nil {
			t.Fatalf("parseCommands(%q) error: %v", src, err)
		}
		if floor(src, cmds) == "" {
			t.Errorf("floor(%q) = allowed, want blocked", src)
		}
	}
	for _, src := range []string{"rm -rf build", "rm -rf ./node_modules", "kill 123", "dd if=a of=b"} {
		cmds, _ := parseCommands(src)
		if reason := floor(src, cmds); reason != "" {
			t.Errorf("floor(%q) = %q, want allowed", src, reason)
		}
	}
}

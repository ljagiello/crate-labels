package service

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestProcessOwner(t *testing.T) {
	me := processOwner()
	pid, start, ok := strings.Cut(me, ":")
	if !ok || pid != strconv.Itoa(os.Getpid()) || start == "" {
		t.Fatalf("owner = %q", me)
	}
	if !ownerAlive(me) {
		t.Fatal("this process is not alive")
	}
	for _, dead := range []string{"", "garbage", "0:0", "-1:5", "99999999:1"} {
		if ownerAlive(dead) {
			t.Errorf("%q counts as alive", dead)
		}
	}
	if start != "0" && ownerAlive(pid+":1") {
		t.Error("a reused pid with another start time counts as alive")
	}
}

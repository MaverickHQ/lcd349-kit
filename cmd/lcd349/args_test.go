package main

import (
	"encoding/json"
	"testing"
)

func TestStatePairs(t *testing.T) {
	got, err := statePairs([]string{"battery.mv=3600", "source=battery", `wifi.networks=[{"ssid":"home","password":"password1"}]`, "board_rev=V2"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	want := `{"battery":{"mv":3600},"board_rev":"V2","source":"battery","wifi":{"networks":[{"password":"password1","ssid":"home"}]}}`
	if string(b) != want {
		t.Fatalf("got %s\nwant %s", b, want)
	}
	if _, err := statePairs([]string{"nokey"}); err == nil {
		t.Fatal("a pair without = was accepted")
	}
	if p, _ := statePairs(nil); p != nil {
		t.Fatal("no pairs should mean state.get")
	}
}

func TestTouchArgs(t *testing.T) {
	tt, err := touchArgs("swipe", []string{"10", "20", "300", "20"}, 400)
	if err != nil || tt.Swipe == nil || tt.Swipe.To[0] != 300 || tt.MS != 400 {
		t.Fatalf("%+v %v", tt, err)
	}
	b, _ := json.Marshal(tt)
	if string(b) != `{"swipe":{"from":[10,20],"to":[300,20]},"ms":400}` {
		t.Fatalf("swipe JSON %s", b)
	}
	if _, err := touchArgs("tap", []string{"x", "1"}, 0); err == nil {
		t.Fatal("a non-number coordinate was accepted")
	}
	tt, err = touchArgs("points", []string{`[{"x":1,"y":2,"down":true},{"x":1,"y":2,"down":false}]`}, 0)
	if err != nil || len(tt.Points) != 2 || !tt.Points[0].Down {
		t.Fatalf("%+v %v", tt, err)
	}
}

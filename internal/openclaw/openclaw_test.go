package openclaw

import (
	"reflect"
	"testing"
)

func TestParseTargets(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"+19198696632", []string{"+19198696632"}},
		{"+19198696632,+2348012345678", []string{"+19198696632", "+2348012345678"}},
		{"  +1234 ,  +5678  ", []string{"+1234", "+5678"}},
		{",,+1234,,", []string{"+1234"}},
		{"  ,  ", nil},
	}
	for _, c := range cases {
		got := ParseTargets(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseTargets(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestNewSkipsEmptyAndTrims(t *testing.T) {
	c := New("+1", "", "  ", "  +2  ")
	want := []string{"+1", "+2"}
	if !reflect.DeepEqual(c.Targets(), want) {
		t.Errorf("Targets() = %v, want %v", c.Targets(), want)
	}
}

func TestSendRejectsEmptyTarget(t *testing.T) {
	c := New("+1")
	err := c.Send(nil, "", "msg", nil)
	if err == nil {
		t.Fatal("expected error on empty target")
	}
}
